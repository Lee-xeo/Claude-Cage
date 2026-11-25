package cage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strings"
	"sync"
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
	return probeExitIP(ctx, e, exitProbeReachabilityURL)
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
// Services span distinct networks and operators (Cloudflare, AWS, Akamai,
// IPv4 literal, ipify, GeoJS) so blocking or throttling one provider does not
// prevent exit sampling.
var exitProbeServices = []exitProbeService{
	{id: "icanhazip", name: "主检测服务", endpoint: "https://ipv4.icanhazip.com/"},
	{id: "amazonaws", name: "AWS 备用服务", endpoint: "https://checkip.amazonaws.com/"},
	{id: "akamai", name: "Akamai 备用服务", endpoint: "https://whatismyip.akamai.com/"},
	{id: "cftrace", name: "IP 直连检测服务", endpoint: "https://1.1.1.1/cdn-cgi/trace", trace: true},
	{id: "ipify", name: "ipify 备用服务", endpoint: "https://api.ipify.org/"},
	{id: "geojs", name: "GeoJS 备用服务", endpoint: "https://ipv4.geojs.io/v1/ip"},
}

// exitProbeReachabilityURL is the fixed target of the node latency test. It is
// requested only after every exit service timed out, to tell a node that
// cannot reach the internet from one whose exit blocks the echo services; its
// response never supplies an exit IP.
const exitProbeReachabilityURL = "https://www.gstatic.com/generate_204"

const (
	// exitProbeRequestTimeout bounds each individual echo service attempt.
	// 7s accommodates higher-RTT residential proxy chains while keeping
	// worst-case all-timeout (5*1s stagger + 7s = 12s) comfortably under
	// diagnosticProbeTimeout (14s).
	exitProbeRequestTimeout = 7 * time.Second
	// A slow service does not hold back the next one. Each service starts when
	// every earlier one has failed or after this delay, whichever comes first.
	// Worst-case all-timeout duration across exitProbeServices stays under
	// diagnosticProbeTimeout so guest/browser probes receive a 503 instead of
	// timing out without a status.
	exitProbeStagger = time.Second
	// diagnosticProbeTimeout bounds one synthetic guest/browser diagnostic probe.
	diagnosticProbeTimeout = 14 * time.Second
)

// An empty reachability URL skips the follow-up request after a timeout.
func probeExitIP(ctx context.Context, e *Egress, reachability string) (string, error) {
	transport := &http.Transport{Proxy: nil, DialContext: e.DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: exitProbeRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("probe redirect refused") }}
	return probeExitIPServices(ctx, client, exitProbeServices, exitProbeStagger, reachability)
}

var (
	// ErrProxyConnectFailed reports that every exit probe failed while connecting
	// through the configured proxy (before TLS or HTTP response).
	ErrProxyConnectFailed = errors.New("proxy connect failed")
	// ErrTLSHandshakeFailed reports that every exit probe failed during TLS
	// handshake after connecting through the configured proxy (for example EOF
	// or timeout caused by large ClientHello packets on some proxy chains).
	ErrTLSHandshakeFailed = errors.New("tls handshake failed")
	// ErrExitProbeTimeout reports that every exit probe timed out waiting for a
	// live exit IP measurement.
	ErrExitProbeTimeout = errors.New("exit probe timed out")
	// ErrExitProbeBlocked reports that every exit probe timed out while the
	// same proxy reached the fixed reachability target, so the exit most
	// likely blocks the echo services rather than the node being down.
	ErrExitProbeBlocked = errors.New("exit probe services blocked by the proxy exit")
)

type exitProbeStageTiming struct {
	stage      string
	dialMS     int64
	tlsMS      int64
	ttfbMS     int64
	proxyError bool
	tlsError   bool
}

type exitProbeResult struct {
	index    int
	ip       string
	err      error
	duration time.Duration
	timing   exitProbeStageTiming
}

// probeExitIPServices returns the first valid public IPv4 reported by any
// service. Every request uses the same proxied client; no result is accepted
// unless it passes the same validation, so a fallback never widens the check.
func probeExitIPServices(ctx context.Context, client *http.Client, services []exitProbeService, stagger time.Duration, reachability string) (string, error) {
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan exitProbeResult, len(services))
	start := func(index int) {
		go func() {
			started := time.Now()
			ip, timing, err := queryExitIPServiceTimed(requestCtx, client, services[index])
			results <- exitProbeResult{index: index, ip: ip, err: err, duration: time.Since(started), timing: timing}
		}()
	}
	failures := make([]string, len(services))
	allTimedOut := true
	allProxyConnectFailed := true
	allTLSHandshakeFailed := true
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
		diagnostics.Log("warn", "exit-probe", fmt.Sprintf("service=%s stage=%s dial_ms=%d tls_ms=%d ttfb_ms=%d duration_ms=%d error=%v cause=%v", service.id, result.timing.stage, result.timing.dialMS, result.timing.tlsMS, result.timing.ttfbMS, result.duration.Milliseconds(), result.err, errors.Unwrap(result.err)))
		failures[result.index] = service.name + "：" + result.err.Error()
		var networkError net.Error
		if !errors.Is(result.err, context.DeadlineExceeded) && !(errors.As(result.err, &networkError) && networkError.Timeout()) {
			allTimedOut = false
		}
		if !result.timing.proxyError {
			allProxyConnectFailed = false
		}
		if !result.timing.tlsError {
			allTLSHandshakeFailed = false
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
	if allTLSHandshakeFailed && !allTimedOut {
		// The bundled core grants a SOCKS5 CONNECT before it dials the node,
		// so a refused, unauthenticated or unroutable node also ends here as
		// an EOF in the handshake. Only a proxy that reaches the plain
		// connectivity address has a handshake problem.
		if reachability != "" && ctx.Err() == nil {
			if reached, _ := proxyReaches(ctx, client, reachability); !reached {
				return "", usererror.New("代理节点无法连通", strings.Join(reported, "；"), "通过所选代理访问连通性检查地址也未成功，节点可能无法连通；请检查节点地址、端口、账号密码以及本机到节点的网络，或切换节点；外网保持关闭。", ErrProxyConnectFailed)
			}
		}
		return "", usererror.New("TLS 握手失败", strings.Join(reported, "；"), "所选代理链路在 TLS 握手阶段中断（如返回 EOF），可尝试在“设置 → 网络与代理”开启“TLS ClientHello 分段传输兼容模式”；若使用未设置 client-fingerprint 的 VMess/VLESS/Trojan 等 TLS/gRPC 节点，也可尝试在节点 YAML 中添加 client-fingerprint: safari 或 firefox 后重新导入并应用，或切换代理节点；外网保持关闭。", ErrTLSHandshakeFailed)
	}
	if allTimedOut {
		title := "实时出口采样超时"
		next := "实时出口采样超时，请稍后重试或切换更稳定的代理节点；外网保持关闭。"
		cause := errors.Join(ErrExitProbeTimeout, context.DeadlineExceeded)
		if allTLSHandshakeFailed {
			title = "TLS 握手超时"
			next = "所选代理链路在 TLS 握手阶段超时，可尝试在“设置 → 网络与代理”开启“TLS ClientHello 分段传输兼容模式”；若使用未设置 client-fingerprint 的 VMess/VLESS/Trojan 等 TLS/gRPC 节点，也可尝试在节点 YAML 中添加 client-fingerprint: safari 或 firefox 后重新导入并应用，或切换代理节点；外网保持关闭。"
			cause = errors.Join(ErrTLSHandshakeFailed, cause)
		}
		if reachability != "" && ctx.Err() == nil {
			// Through the bundled core a SOCKS5 CONNECT is granted before the
			// node is dialed, so an unreachable node and an exit that blocks
			// the echo services both end as response timeouts.
			switch reached, timedOut := proxyReaches(ctx, client, reachability); {
			case reached:
				title = "实时出口采样超时"
				next = "所选代理可以访问连通性检查地址，但出口 IP 检测服务均无响应，可能被节点出口按域名屏蔽；请更换节点，或请代理服务商放行 ipv4.icanhazip.com、checkip.amazonaws.com、whatismyip.akamai.com、1.1.1.1、api.ipify.org 或 ipv4.geojs.io 中的任一地址；外网保持关闭。"
				cause = errors.Join(ErrExitProbeBlocked, cause)
			case timedOut:
				next = "通过所选代理访问连通性检查地址同样超时，节点可能无法连通；请检查节点地址、端口、账号密码以及本机到节点的网络，或切换节点；若节点使用 TLS/gRPC 且未设置 client-fingerprint（如 VMess+gRPC+TLS），可尝试在节点 YAML 中添加 client-fingerprint: safari 或 firefox 后重新导入并应用，或尝试在“设置 → 网络与代理”开启“TLS ClientHello 分段传输兼容模式”；外网保持关闭。"
			}
		}
		return "", usererror.New(title, strings.Join(reported, "；"), next, cause)
	}
	if allProxyConnectFailed {
		return "", usererror.New("代理连接失败", strings.Join(reported, "；"), "未能通过所选代理建立连接，请检查代理核心与节点状态；外网保持关闭。", ErrProxyConnectFailed)
	}
	return "", errors.New("通过所选代理查询出口 IP 失败：" + strings.Join(reported, "；") + "。未能确认出口时，外网保持关闭。")
}

// proxyReaches reports whether one GET to endpoint returned HTTP 204 through
// the client, and otherwise whether the request timed out.
func proxyReaches(ctx context.Context, client *http.Client, endpoint string) (reached, timedOut bool) {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, false
	}
	resp, err := client.Do(req)
	if ctx.Err() != nil {
		// The caller's deadline says nothing about the node.
		if err == nil {
			_ = resp.Body.Close()
		}
		return false, false
	}
	if err != nil {
		var netErr net.Error
		timedOut = errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout()
		diagnostics.Log("warn", "exit-probe", fmt.Sprintf("service=reachability duration_ms=%d timeout=%t error=%v", time.Since(started).Milliseconds(), timedOut, usererror.Reason(err)))
		return false, timedOut
	}
	_ = resp.Body.Close()
	diagnostics.Log("info", "exit-probe", fmt.Sprintf("service=reachability duration_ms=%d status=%d", time.Since(started).Milliseconds(), resp.StatusCode))
	return resp.StatusCode == http.StatusNoContent, false
}

func queryExitIP(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	ip, _, err := queryExitIPServiceTimed(ctx, client, exitProbeService{endpoint: endpoint})
	return ip, err
}

func queryExitIPService(ctx context.Context, client *http.Client, service exitProbeService) (string, error) {
	ip, _, err := queryExitIPServiceTimed(ctx, client, service)
	return ip, err
}

func queryExitIPServiceTimed(ctx context.Context, client *http.Client, service exitProbeService) (string, exitProbeStageTiming, error) {
	// The transport may still be dialing in the background after Do returns
	// (a canceled or timed-out request leaves its dial running), and the trace
	// callbacks then run on that goroutine. Every field the callbacks touch is
	// guarded, and the result is read as a snapshot under the same lock.
	var (
		mu                                                               sync.Mutex
		timing                                                           exitProbeStageTiming
		connectStart, tlsStart, wroteRequest                             time.Time
		connected, tlsStarted, tlsDone, gotFirstByte, connectFailedEarly bool
	)
	timing.stage = "proxy_connect"
	snapshot := func() exitProbeStageTiming {
		mu.Lock()
		defer mu.Unlock()
		return timing
	}
	trace := &httptrace.ClientTrace{
		ConnectStart: func(_, _ string) {
			mu.Lock()
			defer mu.Unlock()
			connectStart = time.Now()
		},
		ConnectDone: func(_, _ string, err error) {
			mu.Lock()
			defer mu.Unlock()
			if !connectStart.IsZero() {
				timing.dialMS = time.Since(connectStart).Milliseconds()
			}
			if err != nil {
				connectFailedEarly = true
				return
			}
			connected = true
			timing.stage = "response"
		},
		TLSHandshakeStart: func() {
			mu.Lock()
			defer mu.Unlock()
			tlsStart = time.Now()
			tlsStarted = true
			timing.stage = "tls"
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			mu.Lock()
			defer mu.Unlock()
			if !tlsStart.IsZero() {
				timing.tlsMS = time.Since(tlsStart).Milliseconds()
			}
			if err == nil {
				tlsDone = true
				timing.stage = "response"
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			mu.Lock()
			defer mu.Unlock()
			wroteRequest = time.Now()
			if info.Err == nil && (!tlsStarted || tlsDone) {
				timing.stage = "response"
			}
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			defer mu.Unlock()
			gotFirstByte = true
			if !wroteRequest.IsZero() {
				timing.ttfbMS = time.Since(wroteRequest).Milliseconds()
			} else if !connectStart.IsZero() {
				timing.ttfbMS = time.Since(connectStart).Milliseconds()
			}
			timing.stage = "response"
		},
	}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", service.endpoint, nil)
	resp, err := client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		var netErr net.Error
		timedOut := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
		mu.Lock()
		result := timing
		if !connected || connectFailedEarly {
			result.stage = "proxy_connect"
			if !timedOut {
				result.proxyError = true
			}
		} else if tlsStarted && !tlsDone && !gotFirstByte {
			result.stage = "tls"
			result.tlsError = true
		} else {
			result.stage = "response"
		}
		mu.Unlock()
		return "", result, usererror.Display("通过所选代理查询出口 IP", err, "请检查网络与代理，再重新检测。")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", snapshot(), fmt.Errorf("查询出口 IP 失败：检测服务返回 HTTP %d。请检查所选代理，稍后重新检测；外网保持关闭。", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return "", snapshot(), usererror.Wrap("读取出口检测响应", err, "请稍后重新检测。")
	}
	if len(body) > 4096 {
		return "", snapshot(), errors.New("查询出口 IP 失败：检测服务没有返回有效的 IP 记录。请更换可用节点或稍后重新检测；外网保持关闭。")
	}
	text := strings.TrimSpace(string(body))
	if service.trace {
		text = traceIP(string(body))
	}
	ip, err := netip.ParseAddr(text)
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "", snapshot(), errors.New("出口检测结果不是有效公网 IPv4 地址")
	}
	return ip.String(), snapshot(), nil
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
	_ = conn.SetDeadline(time.Now().Add(diagnosticProbeTimeout + time.Second))
	ctx, cancel := context.WithTimeout(parent, diagnosticProbeTimeout)
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
	// The guest diagnostic reports only availability, so it skips the
	// follow-up reachability request that classifies a timeout.
	ip, err := probeExitIP(ctx, e, "")
	g.mu.Lock()
	g.state.Counters.ProbeTCP++
	if fault && err != nil {
		g.state.Counters.FaultTCP++
	}
	g.mu.Unlock()
	if err != nil {
		writeDiagnostic(conn, 503, map[string]string{
			"error":  "proxy_unavailable",
			"reason": diagnosticProbeReason(err),
		})
		return
	}
	writeDiagnostic(conn, 200, map[string]string{"ip": ip})
}

// diagnosticProbeReason maps an exit-probe error to a fixed, bounded reason
// token for the internal diagnostic channel. It never includes raw errors, URLs,
// addresses, or credentials.
func diagnosticProbeReason(err error) string {
	switch {
	case errors.Is(err, ErrTLSHandshakeFailed) && errors.Is(err, ErrExitProbeTimeout):
		return "tls_timeout"
	case errors.Is(err, ErrTLSHandshakeFailed):
		return "tls_handshake"
	case errors.Is(err, ErrExitProbeTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrProxyConnectFailed):
		return "proxy_connect"
	default:
		return "probe_failed"
	}
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
