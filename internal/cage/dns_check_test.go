package cage

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func TestCheckDNSEndpointsClassifiesEachEndpointThroughProxy(t *testing.T) {
	// 1. TLS handshake failure (server accepts TCP and immediately closes).
	tlsFailListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tlsFailListener.Close() })
	go func() {
		for {
			conn, err := tlsFailListener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	tlsFailURL := "https://" + tlsFailListener.Addr().String() + "/secret-token/dns-query"

	// 2. Working DoH returning valid public IPv4 A record (mixed with a Fake-IP that must be filtered out).
	okServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil {
			t.Error(err)
			return
		}
		reply := new(dns.Msg)
		reply.SetReply(q)
		reply.Answer = []dns.RR{
			&dns.A{
				Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP("198.18.0.1").To4(),
			},
			&dns.A{
				Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP("151.101.2.132").To4(),
			},
		}
		wire, _ := reply.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	}))
	t.Cleanup(okServer.Close)

	// 3. DNS REFUSED response.
	refusedServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		_ = q.Unpack(body)
		reply := new(dns.Msg)
		reply.SetRcode(q, dns.RcodeRefused)
		wire, _ := reply.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	}))
	t.Cleanup(refusedServer.Close)

	// 4. Empty NOERROR answer (no A record).
	emptyServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		_ = q.Unpack(body)
		reply := new(dns.Msg)
		reply.SetReply(q)
		wire, _ := reply.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	}))
	t.Cleanup(emptyServer.Close)

	// 5. HTTP 503 response.
	httpErrServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(httpErrServer.Close)

	p, connects := testProxy(t)
	cfg := DefaultConfig()
	cfg.ProxyURL = p.URL
	cfg.DNSURL = tlsFailURL
	cfg.DNS = NewDNSOptions([]string{
		okServer.URL + "/personal-id/dns-query",
		refusedServer.URL + "/dns-query",
		emptyServer.URL + "/dns-query",
		httpErrServer.URL + "/dns-query",
	}, nil, false)

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.client.CloseIdleConnections()
	pool := x509.NewCertPool()
	for _, srv := range []*httptest.Server{okServer, refusedServer, emptyServer, httpErrServer} {
		pool.AddCert(srv.Certificate())
	}
	e.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}

	res, err := e.CheckDNSEndpoints(t.Context(), cfg, DefaultDNSCheckTarget)
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != DefaultDNSCheckTarget || !res.OK {
		t.Fatalf("expected OK=true for %s, got %+v", DefaultDNSCheckTarget, res)
	}
	if len(res.Endpoints) != 5 {
		t.Fatalf("expected 5 endpoint results, got %d: %+v", len(res.Endpoints), res.Endpoints)
	}
	if connects.Load() < 5 {
		t.Fatalf("expected all 5 endpoints to traverse proxy, connects=%d", connects.Load())
	}

	// Verify exact classifications and path redaction (personal IDs never leaked).
	checks := []struct {
		idx    int
		role   string
		status string
		detail string
		addr   string
	}{
		{0, "primary", DNSCheckConnection, "tls", ""},
		{1, "fallback", DNSCheckOK, "", "151.101.2.132"},
		{2, "fallback", DNSCheckResponse, "REFUSED", ""},
		{3, "fallback", DNSCheckNoAddress, "", ""},
		{4, "fallback", DNSCheckResponse, "HTTP 503", ""},
	}
	for _, want := range checks {
		got := res.Endpoints[want.idx]
		if got.Role != want.role || got.Status != want.status || got.Detail != want.detail {
			t.Errorf("endpoint[%d]: got role=%q status=%q detail=%q; want role=%q status=%q detail=%q",
				want.idx, got.Role, got.Status, got.Detail, want.role, want.status, want.detail)
		}
		if want.addr != "" && (len(got.Addresses) != 1 || got.Addresses[0] != want.addr) {
			t.Errorf("endpoint[%d]: addresses=%v want=[%s]", want.idx, got.Addresses, want.addr)
		}
		if strings.Contains(got.Host, "/") || strings.Contains(got.Host, "secret-token") || strings.Contains(got.Host, "personal-id") {
			t.Errorf("endpoint[%d]: host leaked path: %q", want.idx, got.Host)
		}
	}

	// When proxy goes down, CheckDNSEndpoints reports OK=false without direct fallback.
	p.Close()
	downRes, err := e.CheckDNSEndpoints(t.Context(), cfg, DefaultDNSCheckTarget)
	if err != nil {
		t.Fatal(err)
	}
	if downRes.OK {
		t.Fatalf("expected OK=false during proxy outage, got %+v", downRes)
	}
	for i, ep := range downRes.Endpoints {
		if ep.Status != DNSCheckConnection || ep.Detail != "connection" {
			t.Errorf("outage endpoint[%d]: status=%q detail=%q want=connection/connection", i, ep.Status, ep.Detail)
		}
	}
}
