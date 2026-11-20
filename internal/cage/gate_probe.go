package cage

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

//go:embed browser-check.html
var browserCheckPage []byte

// ProbeExitIP sends a fixed request through the configured proxy. It does not
// include user headers, credentials, a report or arbitrary guest data.
func ProbeExitIP(ctx context.Context, cfg Config) (string, error) {
	e, err := NewEgress(cfg)
	if err != nil {
		return "", err
	}
	return probeExitIP(ctx, e)
}

// exitProbeService is a fixed public IPv4 echo endpoint. Each one is reached
// only through the selected proxy, so any of them measures the same egress.
type exitProbeService struct {
	id, name, endpoint string
	// trace marks Cloudflare's key=value trace format, which carries the
	// client address on a single "ip=" line.
	trace bool
}

// The proxy resolves the names remotely. IPv4-only endpoints measure the same
// address family as the guest policy even when the proxy is dual-stack; the
// trace endpoint is an IPv4 literal, so its "ip=" line is the IPv4 exit.
var exitProbeServices = []exitProbeService{
	{id: "icanhazip", name: "主检测服务", endpoint: "https://ipv4.icanhazip.com/"},
	{id: "ipify", name: "备用检测服务", endpoint: "https://api.ipify.org/"},
	{id: "cftrace", name: "第三检测服务", endpoint: "https://1.1.1.1/cdn-cgi/trace", trace: true},
}

// A slow service does not hold back the next one. Each service starts when
// every earlier one has failed or after this delay, whichever comes first.
const exitProbeStagger = 1500 * time.Millisecond

func probeExitIP(ctx context.Context, e *Egress) (string, error) {
	transport := &http.Transport{Proxy: nil, DialContext: e.DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("probe redirect refused") }}
	return probeExitIPServices(ctx, client, exitProbeServices, exitProbeStagger)
}

type exitProbeResult struct {
	index    int
	ip       string
	err      error
	duration time.Duration
}

// probeExitIPServices returns the first valid public IPv4 reported by any
// service. Every request uses the same proxied client; no result is accepted
// unless it passes the same validation, so a fallback never widens the check.
func probeExitIPServices(ctx context.Context, client *http.Client, services []exitProbeService, stagger time.Duration) (string, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan exitProbeResult, len(services))
	start := func(index int) {
		go func() {
			started := time.Now()
			ip, err := queryExitIPService(requestCtx, client, services[index])
			results <- exitProbeResult{index: index, ip: ip, err: err, duration: time.Since(started)}
		}()
	}
	failures := make([]string, len(services))
	allTimedOut := true
	started, pending := 1, 1
	start(0)
	timer := time.NewTimer(stagger)
	defer timer.Stop()
	for pending > 0 {
		var result exitProbeResult
		select {
		case result = <-results:
		case <-timer.C:
			if started < len(services) && ctx.Err() == nil {
				start(started)
				started, pending = started+1, pending+1
				timer.Reset(stagger)
			}
			continue
		case <-ctx.Done():
			// Wait for started requests so diagnostics stay complete; their
			// shared context is already canceled.
			cancel()
			result = <-results
		}
		pending--
		if result.err == nil {
			return result.ip, nil
		}
		service := services[result.index]
		// Fixed service IDs survive URL redaction in exported diagnostics.
		diagnostics.Log("warn", "exit-probe", fmt.Sprintf("service=%s duration_ms=%d error=%v cause=%v", service.id, result.duration.Milliseconds(), result.err, errors.Unwrap(result.err)))
		failures[result.index] = service.name + "：" + result.err.Error()
		var networkError net.Error
		if !errors.Is(result.err, context.DeadlineExceeded) && !(errors.As(result.err, &networkError) && networkError.Timeout()) {
			allTimedOut = false
		}
		if pending == 0 && started < len(services) && ctx.Err() == nil {
			start(started)
			started, pending = started+1, pending+1
			timer.Reset(stagger)
		}
	}
	var reported []string
	for _, failure := range failures {
		if failure != "" {
			reported = append(reported, failure)
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "", usererror.Wrap("通过所选代理查询出口 IP", ctx.Err(), "外网保持关闭。")
	}
	if allTimedOut {
		return "", usererror.New("通过所选代理查询出口 IP", strings.Join(reported, "；"), "出口检测超时，请稍后重试。", context.DeadlineExceeded)
	}
	return "", errors.New("通过所选代理查询出口 IP 失败：" + strings.Join(reported, "；") + "。未能确认出口时，外网保持关闭。")
}

func queryExitIP(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	return queryExitIPService(ctx, client, exitProbeService{endpoint: endpoint})
}

func queryExitIPService(ctx context.Context, client *http.Client, service exitProbeService) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", service.endpoint, nil)
	resp, err := client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return "", usererror.Display("通过所选代理查询出口 IP", err, "请检查网络与代理，再重新检测。")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("查询出口 IP 失败：检测服务返回 HTTP %d。请检查所选代理，稍后重新检测；外网保持关闭。", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return "", usererror.Wrap("读取出口检测响应", err, "请稍后重新检测。")
	}
	if len(body) > 4096 {
		return "", errors.New("查询出口 IP 失败：检测服务没有返回有效的 IP 记录。请更换可用节点或稍后重新检测；外网保持关闭。")
	}
	text := strings.TrimSpace(string(body))
	if service.trace {
		text = traceIP(string(body))
	}
	ip, err := netip.ParseAddr(text)
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "", errors.New("出口检测结果不是有效公网 IPv4 地址")
	}
	return ip.String(), nil
}

// traceIP returns the value of the single "ip=" line, or "" when the line is
// missing or repeated. Other trace fields are ignored and never reported.
func traceIP(body string) string {
	var ip string
	found := 0
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "ip="); ok {
			ip, found = strings.TrimSpace(value), found+1
		}
	}
	if found != 1 {
		return ""
	}
	return ip
}

func probeDNS(ctx context.Context, e *Egress) error {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	r, err := e.ExchangeDNS(ctx, q)
	if err != nil {
		return err
	}
	if r.Rcode != dns.RcodeSuccess {
		return errors.New("DNS probe did not resolve its fixed target")
	}
	for _, answer := range r.Answer {
		if a, ok := answer.(*dns.A); ok && a.A.IsGlobalUnicast() && !a.A.IsPrivate() {
			return nil
		}
	}
	return errors.New("DNS probe returned no usable answer")
}

func (g *EgressGate) dialDiagnostic(ctx context.Context) (net.Conn, error) {
	_, done, epoch, err := g.operation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer done()
	client, server := net.Pipe()
	tracked, err := g.track(client, epoch)
	if err != nil {
		server.Close()
		return nil, err
	}
	g.mu.Lock()
	if !g.validLocked(epoch) {
		g.mu.Unlock()
		_ = tracked.Close()
		_ = server.Close()
		return nil, ErrGateClosed
	}
	parent, fault := g.epochContext, g.state.Fault
	g.mu.Unlock()
	go g.serveDiagnostic(parent, server, fault)
	return tracked, nil
}

// This synthetic HTTP service terminates guest input locally. It constructs
// fixed upstream probes instead of forwarding a CONNECT target, URL, query,
// body or headers. It is not a proxy to allowlisted shared CDN addresses.
func (g *EgressGate) serveDiagnostic(parent context.Context, conn net.Conn, fault bool) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	ctx, cancel := context.WithTimeout(parent, 14*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(conn, 8192)))
	if err != nil {
		return
	}
	defer req.Body.Close()
	host := strings.TrimSuffix(strings.ToLower(req.Host), ":80")
	if req.Method != "GET" || host != DiagnosticHost || req.URL.IsAbs() || req.URL.RawQuery != "" || req.ContentLength > 0 || len(req.TransferEncoding) != 0 ||
		(req.URL.Path != "/" && req.URL.Path != "/egress" && req.URL.Path != "/dns" && req.URL.Path != "/browser-check") {
		writeDiagnostic(conn, 400, map[string]string{"error": "fixed_probe_only"})
		return
	}
	if req.URL.Path == "/browser-check" {
		writeDiagnosticResponse(conn, 200, "text/html; charset=utf-8", browserCheckPage)
		return
	}
	if req.URL.Path == "/" {
		writeDiagnostic(conn, 200, map[string]string{"service": "cage-diagnostic"})
		return
	}
	g.mu.Lock()
	if g.probeRequests >= 32 {
		g.mu.Unlock()
		writeDiagnostic(conn, 429, map[string]string{"error": "probe_limit"})
		return
	}
	g.probeRequests++
	g.mu.Unlock()
	e := g.diagnostic
	if fault {
		e = g.failedDiagnostic
	}
	if req.URL.Path == "/dns" {
		err = probeDNS(ctx, e)
		g.mu.Lock()
		g.state.Counters.ProbeDNS++
		if fault && err != nil {
			g.state.Counters.FaultDNS++
		}
		g.mu.Unlock()
		if err != nil {
			writeDiagnostic(conn, 503, map[string]string{"error": "dns_unavailable"})
			return
		}
		writeDiagnostic(conn, 200, map[string]bool{"ok": true})
		return
	}
	ip, err := probeExitIP(ctx, e)
	g.mu.Lock()
	g.state.Counters.ProbeTCP++
	if fault && err != nil {
		g.state.Counters.FaultTCP++
	}
	g.mu.Unlock()
	if err != nil {
		writeDiagnostic(conn, 503, map[string]string{"error": "proxy_unavailable"})
		return
	}
	writeDiagnostic(conn, 200, map[string]string{"ip": ip})
}

func writeDiagnostic(conn net.Conn, status int, value any) {
	body, _ := json.Marshal(value)
	writeDiagnosticResponse(conn, status, "application/json", body)
}

func writeDiagnosticResponse(conn net.Conn, status int, contentType string, body []byte) {
	resp := http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {contentType}, "X-Content-Type-Options": {"nosniff"}, "Referrer-Policy": {"no-referrer"}, "Cache-Control": {"no-store"}, "Access-Control-Allow-Origin": {"*"}, "Connection": {"close"}},
		Body:   io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Close: true}
	_ = resp.Write(conn)
}
