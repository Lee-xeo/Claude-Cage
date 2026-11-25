package cage

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gvDNS "github.com/containers/gvisor-tap-vsock/pkg/services/dns"
	"github.com/miekg/dns"
)

func dohServer(t *testing.T, fail *atomic.Bool, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		if q.Unpack(body) != nil {
			t.Error("invalid DNS request")
			return
		}
		body, _ = new(dns.Msg).SetReply(q).Pack()
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestDNSFailsOverToBackupEndpointThroughProxy(t *testing.T) {
	var primaryDown, backupDown atomic.Bool
	var primaryHits, backupHits atomic.Int32
	primaryDown.Store(true)
	primary := dohServer(t, &primaryDown, &primaryHits)
	backup := dohServer(t, &backupDown, &backupHits)
	p, connects := testProxy(t)
	defer p.Close()
	c := DefaultConfig()
	c.ProxyURL, c.DNSURL = p.URL, primary.URL
	c.DNS = NewDNSOptions([]string{backup.URL}, nil, false)
	e, err := NewEgress(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.client.CloseIdleConnections()
	roots := primary.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	pool := roots.Clone()
	pool.AddCert(backup.Certificate())
	e.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	q := new(dns.Msg).SetQuestion("example.com.", dns.TypeA)
	if _, err := e.ExchangeDNS(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if primaryHits.Load() != 1 || backupHits.Load() != 1 || connects.Load() == 0 {
		t.Fatalf("failover order wrong: primary=%d backup=%d connects=%d", primaryHits.Load(), backupHits.Load(), connects.Load())
	}
	// The endpoint that answered is tried first next time.
	if _, err := e.ExchangeDNS(t.Context(), q); err != nil || primaryHits.Load() != 1 || backupHits.Load() != 2 {
		t.Fatalf("answering endpoint not preferred: %v primary=%d backup=%d", err, primaryHits.Load(), backupHits.Load())
	}
	backupDown.Store(true)
	_, err = e.ExchangeDNS(t.Context(), q)
	if err == nil || !strings.Contains(err.Error(), "设置 → 网络与代理 → 加密 DNS") || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("all endpoints down must point to the settings: %v", err)
	}
	if strings.Contains(err.Error(), "/dns-query") || strings.Contains(err.Error(), primary.URL) {
		t.Fatalf("error leaked an endpoint path: %v", err)
	}
}

func TestDNSEndpointsDefaultsAndOptIn(t *testing.T) {
	c := DefaultConfig()
	if got := c.DNSEndpoints(); !reflect.DeepEqual(got, []string{"https://1.1.1.1/dns-query"}) {
		t.Fatalf("default must stay a single provider: %v", got)
	}
	if got := c.BootstrapEndpoints(); !reflect.DeepEqual(got, DefaultBootstrapDNS) {
		t.Fatalf("bootstrap default changed: %v", got)
	}
	c.DNS = NewDNSOptions([]string{" https://dns.example/dns-query ", "https://1.1.1.1/dns-query", ""}, []string{"https://9.9.9.9/dns-query"}, true)
	if got := c.DNSEndpoints(); !reflect.DeepEqual(got, []string{"https://1.1.1.1/dns-query", "https://dns.example/dns-query", PublicDNSFallback}) {
		t.Fatalf("endpoint order: %v", got)
	}
	if got := c.BootstrapEndpoints(); !reflect.DeepEqual(got, []string{"https://9.9.9.9/dns-query"}) {
		t.Fatalf("bootstrap override: %v", got)
	}
	base := DefaultConfig()
	if base.Fingerprint() == c.Fingerprint() {
		t.Fatal("changing DNS options must change Config.Fingerprint")
	}
	for _, bad := range []DNSOptions{
		NewDNSOptions([]string{"http://dns.example/dns-query"}, nil, false),
		NewDNSOptions([]string{"https://user@dns.example/dns-query"}, nil, false),
		NewDNSOptions(nil, []string{"https://dns.example/dns-query"}, false),
		NewDNSOptions([]string{"https://a/", "https://b/", "https://c/", "https://d/", "https://e/"}, nil, false),
	} {
		c.DNS = bad
		if c.Validate() == nil {
			t.Fatalf("invalid options accepted: %+v", bad)
		}
	}
}

func TestDNSOptionsFileKeepsMainConfigReadableByOlderVersions(t *testing.T) {
	dir := t.TempDir()
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Fatalf("test directory %q outside temp", dir)
	}
	path := filepath.Join(dir, "config.json")
	c := DefaultConfig()
	b, _ := json.Marshal(c)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	o := NewDNSOptions([]string{"https://dns.example/dns-query"}, []string{"https://223.5.5.5/dns-query"}, true)
	if err := SaveDNSOptions(path, o); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.DNS != o {
		t.Fatalf("options not loaded: %+v %v", loaded.DNS, err)
	}
	main, _ := os.ReadFile(path)
	if strings.Contains(string(main), "dns.example") {
		t.Fatal("options leaked into the strict main configuration")
	}
	if err := SaveDNSOptions(path, DNSOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DNSOptionsFile)); !os.IsNotExist(err) {
		t.Fatal("default options must not leave a file")
	}
	if err := os.WriteFile(filepath.Join(dir, DNSOptionsFile), []byte(`{"unknown":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("malformed options accepted")
	}
}

// One transient primary failure must not keep every later query on the
// backup (which may be a second provider the user opted into).
func TestDNSRetriesPrimaryAfterInterval(t *testing.T) {
	var primaryDown, backupDown atomic.Bool
	var primaryHits, backupHits atomic.Int32
	primaryDown.Store(true)
	primary := dohServer(t, &primaryDown, &primaryHits)
	backup := dohServer(t, &backupDown, &backupHits)
	p, _ := testProxy(t)
	defer p.Close()
	c := DefaultConfig()
	c.ProxyURL, c.DNSURL = p.URL, primary.URL
	c.DNS = NewDNSOptions([]string{backup.URL}, nil, false)
	e, err := NewEgress(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.client.CloseIdleConnections()
	pool := primary.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs.Clone()
	pool.AddCert(backup.Certificate())
	e.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	q := new(dns.Msg).SetQuestion("example.com.", dns.TypeA)
	query := func(wantPrimary, wantBackup int32) {
		t.Helper()
		if _, err := e.ExchangeDNS(t.Context(), q); err != nil {
			t.Fatal(err)
		}
		if primaryHits.Load() != wantPrimary || backupHits.Load() != wantBackup {
			t.Fatalf("primary=%d backup=%d, want %d/%d", primaryHits.Load(), backupHits.Load(), wantPrimary, wantBackup)
		}
	}
	retryDue := func() { e.dnsPrimaryRetry.Store(time.Now().Add(-time.Second).UnixNano()) }
	query(1, 1) // primary fails once, backup answers
	primaryDown.Store(false)
	query(1, 2) // within the interval the backup stays first
	if due := time.Until(time.Unix(0, e.dnsPrimaryRetry.Load())); due < dnsPrimaryRetryInterval-time.Minute || due > dnsPrimaryRetryInterval {
		t.Fatalf("primary retry scheduled in %v, want about %v", due, dnsPrimaryRetryInterval)
	}
	primaryDown.Store(true)
	retryDue()
	query(2, 3) // retry: primary still down, one attempt, then backup
	query(2, 4) // and not retried again before the next interval
	primaryDown.Store(false)
	retryDue()
	query(3, 4) // primary back: it answers and is preferred again
	query(4, 4)
}

func TestDNSFailsOverOnHTTP4xxAndDNSResolverErrorsAndNormalizesWireID(t *testing.T) {
	for _, tc := range []struct {
		name       string
		httpStatus int
		dnsRcode   int
	}{
		{"http 403", http.StatusForbidden, 0},
		{"http 404", http.StatusNotFound, 0},
		{"dns SERVFAIL", http.StatusOK, dns.RcodeServerFailure},
		{"dns REFUSED", http.StatusOK, dns.RcodeRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var primaryHits, backupHits atomic.Int32
			var sawWireIDZero atomic.Bool
			primary := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryHits.Add(1)
				if tc.httpStatus != http.StatusOK {
					w.WriteHeader(tc.httpStatus)
					return
				}
				body, _ := io.ReadAll(r.Body)
				q := new(dns.Msg)
				if err := q.Unpack(body); err != nil {
					t.Error(err)
					return
				}
				reply := new(dns.Msg)
				reply.SetRcode(q, tc.dnsRcode)
				wire, _ := reply.Pack()
				w.Header().Set("Content-Type", "application/dns-message")
				_, _ = w.Write(wire)
			}))
			t.Cleanup(primary.Close)

			backup := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupHits.Add(1)
				body, _ := io.ReadAll(r.Body)
				q := new(dns.Msg)
				if err := q.Unpack(body); err != nil {
					t.Error(err)
					return
				}
				if q.Id == 0 {
					sawWireIDZero.Store(true)
				}
				reply := new(dns.Msg)
				reply.SetReply(q)
				reply.RecursionAvailable = true
				reply.Answer = []dns.RR{&dns.A{
					Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP("151.101.2.132").To4(),
				}}
				wire, _ := reply.Pack()
				w.Header().Set("Content-Type", "application/dns-message")
				_, _ = w.Write(wire)
			}))
			t.Cleanup(backup.Close)

			p, connects := testProxy(t)
			c := DefaultConfig()
			c.ProxyURL, c.DNSURL = p.URL, primary.URL
			c.DNS = NewDNSOptions([]string{backup.URL}, nil, false)
			e, err := NewEgress(c)
			if err != nil {
				t.Fatal(err)
			}
			defer e.client.CloseIdleConnections()
			pool := primary.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs.Clone()
			pool.AddCert(backup.Certificate())
			e.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}

			q := new(dns.Msg).SetQuestion("deb.debian.org.", dns.TypeA)
			q.Id = 0x4242
			reply, err := e.ExchangeDNS(t.Context(), q)
			if err != nil {
				t.Fatalf("failover failed: %v", err)
			}
			if q.Id != 0x4242 || reply.Id != 0x4242 {
				t.Fatalf("query ID mismatch: orig=%#x reply=%#x", q.Id, reply.Id)
			}
			if !sawWireIDZero.Load() {
				t.Fatal("DoH wire query did not normalize ID to 0 per RFC 8484")
			}
			if reply.Rcode != dns.RcodeSuccess || len(reply.Answer) != 1 {
				t.Fatalf("unexpected reply: %+v", reply)
			}
			if primaryHits.Load() != 1 || backupHits.Load() != 1 || connects.Load() == 0 {
				t.Fatalf("wrong hit counts: primary=%d backup=%d connects=%d", primaryHits.Load(), backupHits.Load(), connects.Load())
			}
			// Backup is now preferred on the next query.
			if _, err := e.ExchangeDNS(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			if primaryHits.Load() != 1 || backupHits.Load() != 2 {
				t.Fatalf("backup not preferred after failover: primary=%d backup=%d", primaryHits.Load(), backupHits.Load())
			}
		})
	}

	t.Run("all endpoints returning SERVFAIL preserves DNS SERVFAIL reply without preferring failed endpoint", func(t *testing.T) {
		var hits atomic.Int32
		servfail := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			body, _ := io.ReadAll(r.Body)
			q := new(dns.Msg)
			if err := q.Unpack(body); err != nil {
				t.Error(err)
				return
			}
			reply := new(dns.Msg)
			reply.SetRcode(q, dns.RcodeServerFailure)
			wire, _ := reply.Pack()
			w.Header().Set("Content-Type", "application/dns-message")
			_, _ = w.Write(wire)
		}))
		t.Cleanup(servfail.Close)
		p, _ := testProxy(t)
		c := DefaultConfig()
		c.ProxyURL, c.DNSURL = p.URL, servfail.URL
		e, err := NewEgress(c)
		if err != nil {
			t.Fatal(err)
		}
		defer e.client.CloseIdleConnections()
		e.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: servfail.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
		q := new(dns.Msg).SetQuestion("broken.example.", dns.TypeA)
		q.Id = 0x1357
		reply, err := e.ExchangeDNS(t.Context(), q)
		if err != nil || reply == nil || reply.Rcode != dns.RcodeServerFailure || reply.Id != 0x1357 {
			t.Fatalf("expected SERVFAIL reply with preserved ID, got reply=%+v err=%v", reply, err)
		}
		if hits.Load() != 1 {
			t.Fatalf("single SERVFAIL endpoint queried %d times, want 1", hits.Load())
		}
	})
}

func TestMaintenanceDNSForwarderWithGVisorServerAndEgress(t *testing.T) {
	doh := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil {
			t.Error(err)
			return
		}
		reply := new(dns.Msg)
		reply.SetReply(q)
		reply.RecursionAvailable = true
		reply.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("151.101.2.132").To4(),
		}}
		wire, _ := reply.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	}))
	t.Cleanup(doh.Close)

	p, connects := testProxy(t)
	c := DefaultConfig()
	c.ProxyURL, c.DNSURL = p.URL, doh.URL
	egress, err := NewEgress(c)
	if err != nil {
		t.Fatal(err)
	}
	defer egress.client.CloseIdleConnections()
	egress.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: doh.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}

	udpConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udpConn.Close() })
	tcpLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcpLn.Close() })

	srv, err := gvDNS.New(udpConn, tcpLn, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetExchange(egress.ExchangeDNS)
	go func() { _ = srv.Serve() }()
	go func() { _ = srv.ServeTCP() }()

	udpClient := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	for _, ednsSize := range []uint16{0, 512, 1232} {
		q := new(dns.Msg).SetQuestion("deb.debian.org.", dns.TypeA)
		q.SetEdns0(ednsSize, false)
		reply, _, err := udpClient.Exchange(q, udpConn.LocalAddr().String())
		if err != nil {
			t.Fatalf("UDP query (edns=%d) failed: %v", ednsSize, err)
		}
		if reply.Rcode != dns.RcodeSuccess || len(reply.Answer) != 1 || reply.Answer[0].(*dns.A).A.String() != "151.101.2.132" {
			t.Fatalf("UDP query (edns=%d) wrong answer: %+v", ednsSize, reply)
		}
	}

	// Synthetic AAAA returns NOERROR with RA=true and no AAAA records.
	aaaa := new(dns.Msg).SetQuestion("deb.debian.org.", dns.TypeAAAA)
	aaaaReply, _, err := udpClient.Exchange(aaaa, udpConn.LocalAddr().String())
	if err != nil || aaaaReply.Rcode != dns.RcodeSuccess || len(aaaaReply.Answer) != 0 || !aaaaReply.RecursionAvailable {
		t.Fatalf("AAAA reply unexpected: %+v %v", aaaaReply, err)
	}

	// TCP query also succeeds through the same maintenance forwarder.
	tcpClient := &dns.Client{Net: "tcp", Timeout: 3 * time.Second}
	tcpReply, _, err := tcpClient.Exchange(new(dns.Msg).SetQuestion("deb.debian.org.", dns.TypeA), tcpLn.Addr().String())
	if err != nil || tcpReply.Rcode != dns.RcodeSuccess || len(tcpReply.Answer) != 1 {
		t.Fatalf("TCP reply unexpected: %+v %v", tcpReply, err)
	}
	if connects.Load() == 0 {
		t.Fatal("maintenance DNS forwarder bypassed proxy")
	}

	// When the upstream proxy goes down, the forwarder fails closed with SERVFAIL (RA=true).
	egress.client.CloseIdleConnections()
	p.Close()
	downReply, _, err := udpClient.Exchange(new(dns.Msg).SetQuestion("deb.debian.org.", dns.TypeA), udpConn.LocalAddr().String())
	if err != nil || downReply.Rcode != dns.RcodeServerFailure || len(downReply.Answer) != 0 || !downReply.RecursionAvailable {
		t.Fatalf("outage did not fail closed with SERVFAIL: %+v %v", downReply, err)
	}
}
