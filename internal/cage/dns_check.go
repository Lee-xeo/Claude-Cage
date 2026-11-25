package cage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// DefaultDNSCheckTarget is the Debian package host resolved during initial
// installation; probing it checks the exact DNS path APT relies on.
const DefaultDNSCheckTarget = "deb.debian.org"

const (
	DNSCheckOK         = "ok"
	DNSCheckConnection = "connection"
	DNSCheckResponse   = "response"
	DNSCheckNoAddress  = "no_address"
)

const (
	dnsCheckEndpointTimeout = 6 * time.Second
	dnsCheckMaxAddresses    = 4
)

// DNSEndpointCheck describes one configured DoH endpoint tested through the
// selected proxy. Only the endpoint hostname (never its URL path or query) is
// included so personal resolver IDs stay private.
type DNSEndpointCheck struct {
	Host       string   `json:"host"`
	Role       string   `json:"role"`
	Status     string   `json:"status"`
	Addresses  []string `json:"addresses,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	DurationMS int64    `json:"duration_ms"`
}

// DNSCheckResult reports the per-endpoint encrypted DNS test results.
type DNSCheckResult struct {
	Target    string             `json:"target"`
	OK        bool               `json:"ok"`
	Endpoints []DNSEndpointCheck `json:"endpoints"`
}

// CheckDNS tests every configured encrypted DNS endpoint in cfg through cfg's
// proxy-only transport against DefaultDNSCheckTarget. It never uses the host
// resolver and never modifies saved settings or gate state.
func CheckDNS(ctx context.Context, cfg Config) (DNSCheckResult, error) {
	e, err := NewEgress(cfg)
	if err != nil {
		return DNSCheckResult{}, err
	}
	return e.CheckDNSEndpoints(ctx, cfg, DefaultDNSCheckTarget)
}

// CheckDNSEndpoints queries target (type A) against each configured DoH
// endpoint in order through e's proxy-only transport, classifying connection/TLS
// errors, HTTP/DNS error responses, empty answers, and valid public IPv4 A
// records separately.
func (e *Egress) CheckDNSEndpoints(ctx context.Context, cfg Config, target string) (DNSCheckResult, error) {
	target = strings.TrimSpace(strings.TrimSuffix(target, "."))
	if target == "" {
		target = DefaultDNSCheckTarget
	}
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(target), dns.TypeA)
	q.Id = 0
	wire, err := q.Pack()
	if err != nil {
		return DNSCheckResult{}, errors.New("invalid DNS check target")
	}

	// Clone the egress transport with DisableKeepAlives so every endpoint probe
	// exercises a real TLS handshake through the selected proxy and leaves no
	// idle sockets behind.
	baseTransport, _ := e.client.Transport.(*http.Transport)
	var transport *http.Transport
	if baseTransport != nil {
		transport = baseTransport.Clone()
		transport.Proxy = nil
		transport.DisableKeepAlives = true
	} else {
		transport = &http.Transport{Proxy: nil, DialContext: e.DialContext, ForceAttemptHTTP2: true, DisableKeepAlives: true}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       dnsCheckEndpointTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DNS redirect refused") },
	}

	endpoints := cfg.DNSEndpoints()
	explicit := cleanList(append([]string{cfg.DNSURL}, cfg.DNS.FallbackURLs()...))
	results := make([]DNSEndpointCheck, 0, len(endpoints))
	anyOK := false
	for i, endpoint := range endpoints {
		role := "fallback"
		if i == 0 {
			role = "primary"
		} else if cfg.DNS.PublicFallback && i == len(endpoints)-1 && endpoint == PublicDNSFallback && len(endpoints) > len(explicit) {
			role = "public_fallback"
		}
		item := e.checkOneDNSEndpoint(ctx, client, endpoint, role, wire)
		if item.Status == DNSCheckOK {
			anyOK = true
		}
		results = append(results, item)
	}
	return DNSCheckResult{Target: target, OK: anyOK, Endpoints: results}, nil
}

func (e *Egress) checkOneDNSEndpoint(ctx context.Context, client *http.Client, endpoint, role string, wire []byte) DNSEndpointCheck {
	started := time.Now()
	item := DNSEndpointCheck{
		Host: DNSHost(endpoint),
		Role: role,
	}
	attemptCtx, cancel := context.WithTimeout(ctx, dnsCheckEndpointTimeout)
	defer cancel()

	var tlsStarted, tlsDone atomic.Bool
	traceCtx := httptrace.WithClientTrace(attemptCtx, &httptrace.ClientTrace{
		TLSHandshakeStart: func() {
			tlsStarted.Store(true)
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				tlsDone.Store(true)
			}
		},
	})

	req, err := http.NewRequestWithContext(traceCtx, http.MethodPost, endpoint, bytes.NewReader(wire))
	if err != nil {
		item.Status = DNSCheckConnection
		item.Detail = "connection"
		item.DurationMS = time.Since(started).Milliseconds()
		return item
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := client.Do(req)
	if err != nil {
		item.Status = DNSCheckConnection
		item.Detail = classifyDNSCheckConnectionError(attemptCtx, err, tlsStarted.Load() && !tlsDone.Load())
		item.DurationMS = time.Since(started).Milliseconds()
		return item
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		item.Status = DNSCheckResponse
		item.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		item.DurationMS = time.Since(started).Milliseconds()
		return item
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		item.Status = DNSCheckConnection
		item.Detail = classifyDNSCheckConnectionError(attemptCtx, err, false)
		item.DurationMS = time.Since(started).Milliseconds()
		return item
	}

	reply := new(dns.Msg)
	if err := reply.Unpack(body); err != nil || !reply.Response || reply.Id != 0 {
		item.Status = DNSCheckResponse
		item.Detail = "invalid_response"
		item.DurationMS = time.Since(started).Milliseconds()
		return item
	}
	if reply.Rcode != dns.RcodeSuccess {
		code := dns.RcodeToString[reply.Rcode]
		if code == "" {
			code = fmt.Sprintf("RCODE %d", reply.Rcode)
		}
		item.Status = DNSCheckResponse
		item.Detail = code
		item.DurationMS = time.Since(started).Milliseconds()
		return item
	}

	seen := map[string]bool{}
	for _, rr := range reply.Answer {
		a, ok := rr.(*dns.A)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(a.A)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !e.Allows("tcp", netip.AddrPortFrom(ip, 443)) {
			continue
		}
		text := ip.String()
		if !seen[text] {
			seen[text] = true
			item.Addresses = append(item.Addresses, text)
			if len(item.Addresses) >= dnsCheckMaxAddresses {
				break
			}
		}
	}
	if len(item.Addresses) == 0 {
		item.Status = DNSCheckNoAddress
		item.DurationMS = time.Since(started).Milliseconds()
		return item
	}
	item.Status = DNSCheckOK
	item.DurationMS = time.Since(started).Milliseconds()
	return item
}

func classifyDNSCheckConnectionError(ctx context.Context, err error, tlsHandshake bool) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}
	if tlsHandshake {
		return "tls"
	}
	var recordErr tls.RecordHeaderError
	var alertErr tls.AlertError
	lower := strings.ToLower(err.Error())
	if errors.As(err, &recordErr) || errors.As(err, &alertErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(lower, "tls") || strings.Contains(lower, "x509") || strings.Contains(lower, "handshake") {
		return "tls"
	}
	return "connection"
}
