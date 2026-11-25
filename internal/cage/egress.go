package cage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/netguard"
	"github.com/sylvinus/agent-vm/internal/usererror"
	"golang.org/x/net/proxy"
)

// Egress has one upstream and no direct fallback. It lives in the hostagent,
// outside the VM. The guest never receives the upstream credentials.
type Egress struct {
	upstream *url.URL
	dnsURLs  []string
	// dnsPreferred is the endpoint that answered last, so a blocked primary
	// does not cost every query a timeout. dnsPrimaryRetry (Unix nanoseconds)
	// is when one query next tries the primary endpoint first again, so a
	// single transient failure does not leave every later query on a backup.
	dnsPreferred     atomic.Int32
	dnsPrimaryRetry  atomic.Int64
	client           *http.Client
	public           netguard.Policy
	connect          proxy.ContextDialer
	splitClientHello bool
}

// The SOCKS handshake runs on our socket so TCP half-close remains available
// to the relay after authentication. DialContext returns a net.Conn wrapper.
type socksHandshaker interface {
	DialWithConn(context.Context, net.Conn, string, string) (net.Addr, error)
}

// dnsPrimaryRetryInterval is how long queries stay on a backup DoH endpoint
// before one of them tries the primary first again.
var dnsPrimaryRetryInterval = 5 * time.Minute

// dnsStart returns the endpoint a query tries first: the one that answered
// last, or, once dnsPrimaryRetryInterval has passed since the switch to a
// backup, the primary. Only the query that claims the retry slot tries the
// primary; concurrent queries keep the backup meanwhile.
func (e *Egress) dnsStart() int {
	start := int(e.dnsPreferred.Load())
	if start == 0 {
		return 0
	}
	due := e.dnsPrimaryRetry.Load()
	now := time.Now()
	if now.UnixNano() >= due && e.dnsPrimaryRetry.CompareAndSwap(due, now.Add(dnsPrimaryRetryInterval).UnixNano()) {
		return 0
	}
	return start
}

func (e *Egress) dnsAnswered(index int) {
	if index != 0 && e.dnsPreferred.Swap(int32(index)) == 0 {
		e.dnsPrimaryRetry.Store(time.Now().Add(dnsPrimaryRetryInterval).UnixNano())
		return
	}
	e.dnsPreferred.Store(int32(index))
}

func NewEgress(c Config) (*Egress, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	u, _ := url.Parse(c.ProxyURL)
	e := &Egress{
		upstream:         u,
		dnsURLs:          c.DNSEndpoints(),
		connect:          &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second},
		splitClientHello: c.TLSClientHelloSplit,
	}
	e.client = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: e.DialContext, ForceAttemptHTTP2: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DNS redirect refused") }}
	return e, nil
}

func (e *Egress) wrapConn(conn net.Conn) net.Conn {
	if e.splitClientHello {
		return newTLSClientHelloSplitConn(conn)
	}
	return conn
}

func (e *Egress) Allows(network string, dst netip.AddrPort) bool {
	// UDP and ICMP have no fallback. Guest DNS uses the dedicated DoH path.
	return network == "tcp" && dst.Addr().Unmap().Is4() && e.public.Allows(network, dst)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) CloseWrite() error          { return closeWrite(c.Conn) }

func closeWrite(c net.Conn) error {
	if w, ok := c.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return errors.ErrUnsupported
}

func proxyConnectionError(ctx context.Context, action string, err error) error {
	// Closing a stalled handshake on cancellation can report net.ErrClosed.
	// Preserve the operation deadline so exit monitoring can classify a timeout.
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		// The socket deadline can fire before the context timer publishes Err.
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			err = errors.Join(context.DeadlineExceeded, err)
		}
	}
	return usererror.Wrap(action, err, "请检查代理；请求不会改为直连。")
}

// DialContext is used for all guest TCP connections and DoH. DNS names are
// passed to the proxy without resolving them on the host.
func (e *Egress) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("cage permits TCP only")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if strings.HasPrefix(e.upstream.Scheme, "socks5") {
		var auth *proxy.Auth
		if e.upstream.User != nil {
			p, _ := e.upstream.User.Password()
			auth = &proxy.Auth{User: e.upstream.User.Username(), Password: p}
		}
		d, err := proxy.SOCKS5("tcp", e.upstream.Host, auth, nil)
		if err != nil {
			return nil, errors.New("invalid SOCKS5 configuration")
		}
		handshake, ok := d.(socksHandshaker)
		if !ok {
			return nil, errors.New("SOCKS5 dialer cannot negotiate on an existing connection")
		}
		c, err := e.connect.DialContext(ctx, "tcp", e.upstream.Host)
		if err != nil {
			return nil, proxyConnectionError(ctx, "连接指定 SOCKS5 代理", err)
		}
		if _, err := handshake.DialWithConn(ctx, c, "tcp", addr); err != nil {
			_ = c.Close()
			return nil, proxyConnectionError(ctx, "连接指定 SOCKS5 代理", err)
		}
		return e.wrapConn(c), nil
	}
	conn, err := e.connect.DialContext(ctx, "tcp", e.upstream.Host)
	if err != nil {
		return nil, proxyConnectionError(ctx, "连接指定代理", err)
	}
	// Cancellation must interrupt a stalled CONNECT response, not only the
	// initial TCP connect. Stop the callback before returning a live tunnel.
	upstreamConn := conn
	stopCancel := context.AfterFunc(ctx, func() { _ = upstreamConn.Close() })
	defer stopCancel()
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if deadline, yes := ctx.Deadline(); yes {
		conn.SetDeadline(deadline)
	}
	if e.upstream.Scheme == "https" {
		t := tls.Client(conn, &tls.Config{ServerName: e.upstream.Hostname(), MinVersion: tls.VersionTLS12})
		if err = t.HandshakeContext(ctx); err != nil {
			return nil, proxyConnectionError(ctx, "验证 HTTPS 代理的加密连接", err)
		}
		conn = t
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: make(http.Header)}
	if e.upstream.User != nil {
		p, _ := e.upstream.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(e.upstream.User.Username()+":"+p)))
	}
	if err = req.Write(conn); err != nil {
		return nil, proxyConnectionError(ctx, "发送代理 CONNECT 请求", err)
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, proxyConnectionError(ctx, "读取代理 CONNECT 响应", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("代理拒绝 CONNECT 请求（HTTP %d）；请求不会改为直连", resp.StatusCode)
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return e.wrapConn(&bufferedConn{Conn: conn, reader: r}), nil
}

// ExchangeDNS forwards DNS wire messages over HTTPS via the same upstream.
// It never calls the host resolver, even when the upstream fails.
func (e *Egress) ExchangeDNS(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	for _, question := range q.Question {
		// The initial exit policy monitors IPv4. Do not offer an unmeasured
		// guest IPv6 route; literal IPv6 destinations are denied by Allows too.
		if question.Qtype == dns.TypeAAAA {
			r := new(dns.Msg)
			r.SetReply(q)
			r.RecursionAvailable = true
			return r, nil
		}
		n := strings.ToLower(strings.TrimSuffix(question.Name, "."))
		if n == "localhost" || strings.HasSuffix(n, ".localhost") || strings.HasSuffix(n, ".internal") || strings.HasSuffix(n, ".local") {
			r := new(dns.Msg)
			r.SetRcode(q, dns.RcodeNameError)
			r.RecursionAvailable = true
			return r, nil
		}
	}
	// RFC 8484 section 4.1: DoH requests should carry ID 0 on the wire while
	// preserving the guest query's original ID in the returned reply.
	reqMsg := q.Copy()
	reqMsg.Id = 0
	wire, err := reqMsg.Pack()
	if err != nil {
		return nil, err
	}
	// Keep the original total deadline, but allow a fresh connection after an
	// intermittent EOF or overloaded DoH server. Every attempt uses this same
	// proxy-only transport. Transient failures retry and rotate across endpoints;
	// permanent per-endpoint failures (HTTP 4xx, invalid wire reply, or resolver
	// SERVFAIL/REFUSED/FORMERR) still fail over to remaining configured endpoints
	// without re-querying the failed endpoint.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoints := e.dnsURLs
	start := e.dnsStart()
	attempts := max(3, len(endpoints))
	excluded := make([]bool, len(endpoints))
	var fallbackReply *dns.Msg
	var lastReason string
	var lastErr error
	nextEndpoint := func(from int) int {
		for offset := 0; offset < len(endpoints); offset++ {
			idx := (from + offset) % len(endpoints)
			if !excluded[idx] {
				return idx
			}
		}
		return -1
	}
	cursor := start
	for attempt := 0; attempt < attempts; attempt++ {
		if ctx.Err() != nil {
			err = e.dnsUnavailable(ctx.Err())
			e.logDNSFailure("canceled", err)
			return nil, err
		}
		index := nextEndpoint(cursor)
		if index < 0 {
			break
		}
		cursor = (index + 1) % len(endpoints)
		attemptCtx, done := context.WithTimeout(ctx, 6*time.Second)
		answer, retry, reason, attemptErr := e.exchangeDNSOnce(attemptCtx, endpoints[index], q.Id, wire)
		done()
		if attemptErr == nil {
			if answer.Rcode == dns.RcodeServerFailure || answer.Rcode == dns.RcodeRefused || answer.Rcode == dns.RcodeFormatError {
				fallbackReply = answer
				excluded[index] = true
				lastReason = "dns_rcode_" + dns.RcodeToString[answer.Rcode]
				if nextEndpoint(cursor) < 0 {
					return fallbackReply, nil
				}
				continue
			}
			e.dnsAnswered(index)
			return answer, nil
		}
		lastErr = attemptErr
		lastReason = reason
		if !retry {
			excluded[index] = true
			if nextEndpoint(cursor) < 0 {
				if fallbackReply != nil {
					return fallbackReply, nil
				}
				err = e.dnsUnavailable(lastErr)
				e.logDNSFailure(lastReason, err)
				return nil, err
			}
			continue
		}
		e.client.CloseIdleConnections()
		if attempt < 2 && attempt+1 < attempts {
			select {
			case <-ctx.Done():
				err = e.dnsUnavailable(ctx.Err())
				e.logDNSFailure("canceled", err)
				return nil, err
			case <-time.After(time.Duration(attempt+1) * 150 * time.Millisecond):
			}
		}
	}
	if fallbackReply != nil {
		return fallbackReply, nil
	}
	err = e.dnsUnavailable(lastErr)
	e.logDNSFailure(lastReason, err)
	return nil, err
}

func (e *Egress) logDNSFailure(reason string, cause error) {
	hosts := make([]string, 0, len(e.dnsURLs))
	for _, endpoint := range e.dnsURLs {
		hosts = append(hosts, DNSHost(endpoint))
	}
	if reason == "" {
		reason = "unavailable"
	}
	diagnostics.Log("warn", "dns", fmt.Sprintf("endpoints=%s reason=%s error=%v", strings.Join(hosts, ","), reason, cause))
}

func classifyDNSTransportError(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}
	var certErr tls.RecordHeaderError
	var alertErr tls.AlertError
	if errors.As(err, &certErr) || errors.As(err, &alertErr) || strings.Contains(strings.ToLower(err.Error()), "tls") || strings.Contains(strings.ToLower(err.Error()), "x509") {
		return "tls_handshake"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}
	return "transport"
}

// dnsUnavailable names the endpoints (hosts only) and where to change them.
func (e *Egress) dnsUnavailable(cause error) error {
	hosts := make([]string, 0, len(e.dnsURLs))
	for _, endpoint := range e.dnsURLs {
		hosts = append(hosts, DNSHost(endpoint))
	}
	return fmt.Errorf("经所选节点无法使用加密 DNS（%s）；可在“设置 → 网络与代理 → 加密 DNS”改用其他端点、添加备用端点，或开启公共 DNS 备用：%w", strings.Join(hosts, "、"), cause)
}

func (e *Egress) exchangeDNSOnce(ctx context.Context, endpoint string, origID uint16, wire []byte) (*dns.Msg, bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(wire))
	if err != nil {
		return nil, false, "invalid_request", errors.New("invalid DNS request")
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, true, classifyDNSTransportError(err), errors.New("cage DNS upstream unavailable; check the configured DoH endpoint through the selected proxy")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode == 429 || resp.StatusCode >= 500, fmt.Sprintf("http_%d", resp.StatusCode), fmt.Errorf("cage DNS upstream returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, true, "incomplete_body", errors.New("incomplete DNS upstream response")
	}
	r := new(dns.Msg)
	if err = r.Unpack(body); err != nil {
		return nil, false, "invalid_wire", errors.New("invalid DNS upstream response")
	}
	if !r.Response || r.Id != 0 {
		return nil, false, "mismatched_reply", errors.New("mismatched DNS upstream response")
	}
	r.Id = origID
	return r, false, "", nil
}
