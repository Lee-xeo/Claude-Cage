package cage

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExitProbeClassifiesProxyTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var connections atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() { defer conn.Close(); _, _ = io.Copy(io.Discard, conn) }()
		}
	}()
	cfg := DefaultConfig()
	cfg.ProxyURL = "http://" + listener.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ip, err := ProbeExitIP(ctx, cfg)
	// Every exit service, then one reachability check through the same proxy.
	wantConnections := int32(len(exitProbeServices) + 1)
	if ip != "" || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrExitProbeBlocked) || !strings.Contains(err.Error(), "节点可能无法连通") || connections.Load() != wantConnections {
		t.Fatalf("timed-out proxy probes = %q, %v, connections=%d want=%d", ip, err, connections.Load(), wantConnections)
	}
}

// The child uses a private test CA, so the real ProbeExitIP entry point verifies
// TLS without changing production trust or replacing the HTTP transport.
func TestExitProbeChild(t *testing.T) {
	proxy := os.Getenv("CAGE_EXIT_PROBE_TEST_PROXY")
	if proxy == "" {
		t.Skip("subprocess fixture")
	}
	certificate, err := os.ReadFile(os.Getenv("CAGE_EXIT_PROBE_TEST_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		t.Fatal("invalid test CA")
	}
	x509.SetFallbackRoots(roots)
	cfg := DefaultConfig()
	cfg.ProxyURL = proxy
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	want := os.Getenv("CAGE_EXIT_PROBE_TEST_IP")
	ip, err := ProbeExitIP(ctx, cfg)
	if want != "" {
		if err != nil || ip != want {
			t.Fatalf("host probe = %q, %v; want %q", ip, err, want)
		}
	} else if err == nil || ip != "" || strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), "test-password") {
		t.Fatalf("failed host probe returned an address or leaked a response: %q, %v", ip, err)
	}
	gate, err := NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close("test complete")
	if err := gate.BeginCheck(); err != nil {
		t.Fatal(err)
	}
	connection, err := gate.DialContext(ctx, "tcp", DiagnosticAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(connection, "GET /egress HTTP/1.1\r\nHost: 198.18.0.254\r\nCookie: private-value\r\nAuthorization: private-value\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result struct{ IP, Error string }
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if want != "" {
		if response.StatusCode != 200 || result.IP != want {
			t.Fatalf("guest diagnostic lost IPv4 JSON contract: %d %+v", response.StatusCode, result)
		}
	} else if response.StatusCode != 503 || result.IP != "" || result.Error != "proxy_unavailable" {
		t.Fatalf("failed guest diagnostic did not retain its fixed error contract: %d %+v", response.StatusCode, result)
	}
}

func TestExitProbeUsesIPv4ServiceThroughProxy(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"ipv4.icanhazip.com", "checkip.amazonaws.com", "whatismyip.akamai.com", "api.ipify.org", "ipv4.geojs.io"}, IPAddresses: []net.IP{net.IPv4(1, 1, 1, 1)}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600); err != nil {
		t.Fatal(err)
	}
	expectedURI := map[string]string{
		"ipv4.icanhazip.com":    "/",
		"checkip.amazonaws.com": "/",
		"whatismyip.akamai.com": "/",
		"1.1.1.1":               "/cdn-cgi/trace",
		"api.ipify.org":         "/",
		"ipv4.geojs.io":         "/v1/ip",
	}
	for _, scheme := range []string{"http", "socks5"} {
		for _, tc := range []struct {
			name, body, want, answerHost string
			status                       int
			wantRequests                 int32
		}{
			{"public-ipv4", "8.8.8.8\n", "8.8.8.8", "ipv4.icanhazip.com", 200, 2},
			{"fallback-after-unavailable", "8.8.8.8\n", "8.8.8.8", "checkip.amazonaws.com", 503, 4},
			{"fallback-after-invalid", "8.8.8.8\n", "8.8.8.8", "checkip.amazonaws.com", 200, 4},
			{"fallback-akamai", "8.8.8.8\n", "8.8.8.8", "whatismyip.akamai.com", 503, 6},
			{"trace-after-earlier-fail", "fl=1\nip=8.8.8.8\nuag=Go-http-client/1.1\n", "8.8.8.8", "1.1.1.1", 503, 8},
			{"fallback-ipify", "8.8.8.8\n", "8.8.8.8", "api.ipify.org", 503, 10},
			{"fallback-geojs", "8.8.8.8\n", "8.8.8.8", "ipv4.geojs.io", 503, 12},
			{"trace-repeated-ip", "ip=8.8.8.8\nip=9.9.9.9\n", "", "1.1.1.1", 503, 12},
			{"trace-private-ip", "ip=192.168.1.1\n", "", "1.1.1.1", 503, 12},
			{"whitespace", " \r\n1.1.1.1\r\n", "1.1.1.1", "ipv4.icanhazip.com", 200, 2},
			{"ipv6", "2606:4700:4700::1111\n", "", "ipv4.icanhazip.com", 200, 12},
			{"mapped-ipv4", "::ffff:8.8.8.8", "", "ipv4.icanhazip.com", 200, 12},
			{"private-ipv4", "192.168.1.1", "", "ipv4.icanhazip.com", 200, 12},
			{"loopback", "127.0.0.1", "", "ipv4.icanhazip.com", 200, 12},
			{"link-local", "169.254.1.1", "", "ipv4.icanhazip.com", 200, 12},
			{"multicast", "224.0.0.1", "", "ipv4.icanhazip.com", 200, 12},
			{"empty", "", "", "ipv4.icanhazip.com", 200, 12},
			{"json-response", `{"ip":"8.8.8.8","secret":"private-value"}`, "", "ipv4.icanhazip.com", 200, 12},
			{"multiple-addresses", "8.8.8.8\n1.1.1.1", "", "ipv4.icanhazip.com", 200, 12},
			{"oversized", "8.8.8.8" + strings.Repeat(" ", 4096), "", "ipv4.icanhazip.com", 200, 12},
			{"short-read", "8.8.8.8", "", "ipv4.icanhazip.com", 200, 12},
			{"unavailable", "private-value", "", "ipv4.icanhazip.com", 503, 12},
			{"redirect", "private-value", "", "ipv4.icanhazip.com", 302, 12},
		} {
			t.Run(scheme+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					requests.Add(1)
					wantURI, ok := expectedURI[request.Host]
					if !ok || request.URL.RequestURI() != wantURI || request.Method != "GET" || request.Header.Get("User-Agent") != "Go-http-client/1.1" || request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
						t.Error("probe request changed destination or forwarded guest/proxy data")
					}
					if request.Host != "ipv4.icanhazip.com" {
						if request.Host == tc.answerHost {
							_, _ = io.WriteString(w, tc.body)
							return
						}
						if request.Host == "1.1.1.1" {
							// A bare address is not the trace format.
							_, _ = io.WriteString(w, "8.8.8.8")
							return
						}
						_, _ = io.WriteString(w, `{"ip":"2606:4700:4700::1111"}`)
						return
					}
					if tc.name == "fallback-after-invalid" {
						_, _ = io.WriteString(w, "192.168.1.1")
						return
					}
					if tc.answerHost != "ipv4.icanhazip.com" {
						w.WriteHeader(tc.status)
						return
					}
					w.Header().Set("Content-Type", "text/plain")
					if tc.name == "redirect" {
						w.Header().Set("Location", "https://private-value.invalid/")
					}
					if tc.name == "short-read" {
						w.Header().Set("Content-Length", "50")
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				upstream.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{certificate}, PrivateKey: key}}}
				upstream.StartTLS()
				defer upstream.Close()
				proxy, connects := exitProbeProxy(t, scheme, upstream.Listener.Addr().String())
				command := exec.Command(os.Args[0], "-test.run=^TestExitProbeChild$", "-test.timeout=15s")
				command.Env = append(os.Environ(), "GODEBUG=x509usefallbackroots=1", "CAGE_EXIT_PROBE_TEST_PROXY="+proxy, "CAGE_EXIT_PROBE_TEST_CA="+caFile, "CAGE_EXIT_PROBE_TEST_IP="+tc.want)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("real probe entry failed: %v\n%s", err, output)
				}
				if connects.Load() != tc.wantRequests || requests.Load() != tc.wantRequests {
					t.Fatalf("host and guest probes must use only the configured proxy: connects=%d requests=%d want=%d", connects.Load(), requests.Load(), tc.wantRequests)
				}
			})
		}
	}
}

func TestExitProbeProxyRejectionDoesNotFallbackOrLeakResponse(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "private-proxy-response")
	}))
	defer proxy.Close()
	cfg := DefaultConfig()
	cfg.ProxyURL = proxy.URL
	ip, err := ProbeExitIP(context.Background(), cfg)
	if err == nil || ip != "" || requests.Load() != int32(len(exitProbeServices)) || strings.Contains(err.Error(), "private-proxy-response") {
		t.Fatalf("proxy rejection escaped the configured route or leaked its response: IP=%q error=%v requests=%d", ip, err, requests.Load())
	}
}

func TestExitProbePublicIPv4Service(t *testing.T) {
	if os.Getenv("CAGE_EXIT_PROBE_LIVE") != "1" {
		t.Skip("requires the public IPv4 service through an isolated local proxy")
	}
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			proxy, requests := exitProbeProxy(t, scheme, "ipv4.icanhazip.com:443")
			cfg := DefaultConfig()
			cfg.ProxyURL = proxy
			ip, err := ProbeExitIP(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			address, err := netip.ParseAddr(ip)
			if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() || requests.Load() != 1 {
				t.Fatal("public service did not return IPv4 through the selected proxy")
			}
			t.Log("public endpoint returned a public IPv4 through the isolated proxy")
		})
	}
}

// The local fixture accepts CONNECT or SOCKS domain requests and maps the fixed
// public hostname to an isolated TLS server without using the application's dialer.
func exitProbeProxy(t *testing.T, scheme, upstream string) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	count := new(atomic.Int32)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
				reader := bufio.NewReader(connection)
				var target string
				if scheme == "http" {
					request, err := http.ReadRequest(reader)
					if err != nil || request.Method != "CONNECT" {
						t.Error("expected HTTP CONNECT")
						return
					}
					if request.Header.Get("Proxy-Authorization") != "Basic dGVzdC11c2VyOnRlc3QtcGFzc3dvcmQ=" {
						t.Error("HTTP proxy authentication was lost")
						return
					}
					target = request.Host
					_, _ = io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
				} else {
					var header [4]byte
					if _, err := io.ReadFull(reader, header[:2]); err != nil {
						return
					}
					if _, err := io.CopyN(io.Discard, reader, int64(header[1])); err != nil {
						return
					}
					_, _ = connection.Write([]byte{5, 2})
					if _, err := io.ReadFull(reader, header[:2]); err != nil {
						return
					}
					username := make([]byte, int(header[1]))
					if _, err := io.ReadFull(reader, username); err != nil {
						return
					}
					passwordLength, err := reader.ReadByte()
					if err != nil {
						return
					}
					password := make([]byte, int(passwordLength))
					if _, err := io.ReadFull(reader, password); err != nil {
						return
					}
					if string(username) != "test-user" || string(password) != "test-password" {
						t.Error("SOCKS proxy authentication was lost")
						return
					}
					_, _ = connection.Write([]byte{1, 0})
					if _, err := io.ReadFull(reader, header[:]); err != nil {
						return
					}
					var host []byte
					switch {
					case header[0] == 5 && header[1] == 1 && header[3] == 3:
						length, err := reader.ReadByte()
						if err != nil {
							return
						}
						host = make([]byte, length)
						if _, err := io.ReadFull(reader, host); err != nil {
							return
						}
					case header[0] == 5 && header[1] == 1 && header[3] == 1:
						// Only the fixed trace endpoint is an address literal.
						var address [4]byte
						if _, err := io.ReadFull(reader, address[:]); err != nil {
							return
						}
						host = []byte(netip.AddrFrom4(address).String())
					default:
						t.Error("SOCKS probe resolved its hostname outside the proxy")
						return
					}
					if _, err := io.ReadFull(reader, header[:2]); err != nil {
						return
					}
					target = net.JoinHostPort(string(host), fmt.Sprint(binary.BigEndian.Uint16(header[:2])))
					_, _ = connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
				}
				switch target {
				case "ipv4.icanhazip.com:443", "checkip.amazonaws.com:443", "whatismyip.akamai.com:443", "1.1.1.1:443", "api.ipify.org:443", "ipv4.geojs.io:443":
				default:
					t.Errorf("probe followed a redirect or changed destination: %s", target)
					return
				}
				count.Add(1)
				remote, err := net.DialTimeout("tcp", upstream, time.Second)
				if err != nil {
					t.Error(err)
					return
				}
				defer remote.Close()
				go func() { _, _ = io.Copy(remote, reader); _ = remote.(*net.TCPConn).CloseWrite() }()
				_, _ = io.Copy(connection, remote)
			}()
		}
	}()
	return scheme + "://test-user:test-password@" + listener.Addr().String(), count
}
func TestBrowserCheckServesOnlyFixedPage(t *testing.T) {
	gate, err := NewEgressGate(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close("test complete")
	if err := gate.BeginCheck(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/browser-check", "/browser-check?report=unexpected"} {
		conn, err := gate.DialContext(context.Background(), "tcp", DiagnosticAddress)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", path, DiagnosticHost)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if path == "/browser-check" {
			if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || string(body) != string(browserCheckPage) {
				t.Fatal("normal browser cannot run the fixed diagnostic page")
			}
		} else if resp.StatusCode != 400 {
			t.Fatal("browser callback parameters must stay in the client-side fragment")
		}
	}
}

func TestExitProbeSlowPrimaryDoesNotDelayFallback(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, "9.9.9.9")
	}))
	defer slow.Close()
	defer close(release)
	var fallbackRequests, traceRequests atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackRequests.Add(1)
		_, _ = io.WriteString(w, "8.8.8.8\n")
	}))
	defer fallback.Close()
	trace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceRequests.Add(1)
		_, _ = io.WriteString(w, "ip=1.1.1.1\n")
	}))
	defer trace.Close()
	services := []exitProbeService{
		{id: "slow", name: "slow", endpoint: slow.URL},
		{id: "fallback", name: "fallback", endpoint: fallback.URL},
		{id: "trace", name: "trace", endpoint: trace.URL, trace: true},
	}
	client := &http.Client{Timeout: 5 * time.Second}
	started := time.Now()
	ip, err := probeExitIPServices(context.Background(), client, services, 100*time.Millisecond, "")
	if err != nil || ip != "8.8.8.8" {
		t.Fatalf("staggered probe = %q, %v", ip, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("slow primary held back the fallback for %v", elapsed)
	}
	if fallbackRequests.Load() != 1 || traceRequests.Load() != 0 {
		t.Fatalf("fallback=%d trace=%d; later services must start only when needed", fallbackRequests.Load(), traceRequests.Load())
	}
}

func TestExitProbeFailedServicesStartNextImmediately(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	trace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "fl=1\r\nip=8.8.4.4\r\nts=1\r\n")
	}))
	defer trace.Close()
	services := []exitProbeService{
		{id: "a", name: "a", endpoint: failing.URL},
		{id: "b", name: "b", endpoint: failing.URL},
		{id: "trace", name: "trace", endpoint: trace.URL, trace: true},
	}
	started := time.Now()
	// A long stagger proves the failures, not the timer, advanced the probe.
	ip, err := probeExitIPServices(context.Background(), &http.Client{Timeout: 5 * time.Second}, services, time.Minute, "")
	if err != nil || ip != "8.8.4.4" || time.Since(started) > 5*time.Second {
		t.Fatalf("trace fallback = %q, %v after %v", ip, err, time.Since(started))
	}
	ip, err = probeExitIPServices(context.Background(), &http.Client{Timeout: 5 * time.Second}, services[:2], time.Minute, "")
	if err == nil || ip != "" || !strings.Contains(err.Error(), "HTTP 503") || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("all failed services must stay closed: %q, %v", ip, err)
	}
}

func TestTraceIPRequiresSingleLine(t *testing.T) {
	for body, want := range map[string]string{
		"fl=1\nip=8.8.8.8\nts=1\n": "8.8.8.8",
		"ip=8.8.8.8":               "8.8.8.8",
		"ip=8.8.8.8\nip=9.9.9.9":   "",
		"8.8.8.8":                  "",
		"xip=8.8.8.8":              "",
		"":                         "",
	} {
		if got := traceIP(body); got != want {
			t.Errorf("traceIP(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestExitProbeDistinguishesProxyConnectFailureFromTimeout(t *testing.T) {
	// Closed local port -> immediate proxy connection failure (not timeout).
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := listener.Addr().String()
	_ = listener.Close()

	cfg := DefaultConfig()
	cfg.ProxyURL = "http://" + closedAddr
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ip, err := ProbeExitIP(ctx, cfg)
	if ip != "" || !errors.Is(err, ErrProxyConnectFailed) || errors.Is(err, ErrExitProbeTimeout) || !strings.Contains(err.Error(), "代理连接失败") {
		t.Fatalf("closed proxy expected ErrProxyConnectFailed, got ip=%q err=%v", ip, err)
	}

	// Slow service with short client timeout -> ErrExitProbeTimeout + context.DeadlineExceeded.
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer stalled.Close()
	services := []exitProbeService{{id: "slow", name: "慢检测服务", endpoint: stalled.URL}}
	ip, err = probeExitIPServices(context.Background(), &http.Client{Timeout: 80 * time.Millisecond}, services, time.Minute, "")
	if ip != "" || !errors.Is(err, ErrExitProbeTimeout) || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrProxyConnectFailed) || !strings.Contains(err.Error(), "实时出口采样超时") {
		t.Fatalf("stalled service expected ErrExitProbeTimeout, got ip=%q err=%v", ip, err)
	}
}

func TestExitProbeClassifiesTLSHandshakeEOFAndTimeout(t *testing.T) {
	// Server that accepts TCP and immediately closes (EOF during TLS handshake).
	eofLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer eofLn.Close()
	go func() {
		for {
			c, err := eofLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				_, _ = conn.Read(buf)
			}(c)
		}
	}()
	eofServices := []exitProbeService{{id: "eof", name: "EOF 服务", endpoint: "https://" + eofLn.Addr().String()}}
	ip, err := probeExitIPServices(context.Background(), &http.Client{Timeout: 2 * time.Second}, eofServices, time.Minute, "")
	if ip != "" || !errors.Is(err, ErrTLSHandshakeFailed) || errors.Is(err, ErrProxyConnectFailed) || errors.Is(err, ErrExitProbeTimeout) || !strings.Contains(err.Error(), "TLS 握手失败") || !strings.Contains(err.Error(), "client-fingerprint: safari") {
		t.Fatalf("TLS EOF expected ErrTLSHandshakeFailed with client-fingerprint hint, got ip=%q err=%v", ip, err)
	}

	// Server that accepts TCP and stalls during TLS handshake -> TLS handshake timeout.
	stallLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stallLn.Close()
	stallDone := make(chan struct{})
	defer close(stallDone)
	go func() {
		for {
			c, err := stallLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				<-stallDone
			}(c)
		}
	}()
	stallServices := []exitProbeService{{id: "tls-stall", name: "握手超时服务", endpoint: "https://" + stallLn.Addr().String()}}
	ip, err = probeExitIPServices(context.Background(), &http.Client{Timeout: 120 * time.Millisecond}, stallServices, time.Minute, "")
	if ip != "" || !errors.Is(err, ErrTLSHandshakeFailed) || !errors.Is(err, ErrExitProbeTimeout) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "TLS 握手超时") || !strings.Contains(err.Error(), "client-fingerprint: safari") {
		t.Fatalf("TLS stall expected ErrTLSHandshakeFailed + ErrExitProbeTimeout with client-fingerprint hint, got ip=%q err=%v", ip, err)
	}
}

func TestExitProbeTimeoutChecksWhetherTheProxyReachesTheInternet(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer stalled.Close()
	var checks atomic.Int32
	reachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer reachable.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	services := []exitProbeService{{id: "a", name: "主检测服务", endpoint: stalled.URL}, {id: "b", name: "备用检测服务", endpoint: stalled.URL}}
	probe := func(reachability string) error {
		ip, err := probeExitIPServices(context.Background(), &http.Client{Timeout: 80 * time.Millisecond}, services, time.Minute, reachability)
		if ip != "" || !errors.Is(err, ErrExitProbeTimeout) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "实时出口采样超时") || !strings.Contains(err.Error(), "备用检测服务") {
			t.Fatalf("all timed out services must keep the timeout error: ip=%q err=%v", ip, err)
		}
		return err
	}

	err := probe(reachable.URL)
	if !errors.Is(err, ErrExitProbeBlocked) || !strings.Contains(err.Error(), "可能被节点出口按域名屏蔽") || checks.Load() != 1 {
		t.Fatalf("reachable proxy must report blocked echo services: checks=%d err=%v", checks.Load(), err)
	}
	err = probe(stalled.URL)
	if errors.Is(err, ErrExitProbeBlocked) || !strings.Contains(err.Error(), "节点可能无法连通") || !strings.Contains(err.Error(), "client-fingerprint: safari") {
		t.Fatalf("stalled reachability check must point at the node and include client-fingerprint hint: %v", err)
	}
	for _, reachability := range []string{other.URL, ""} {
		err = probe(reachability)
		if errors.Is(err, ErrExitProbeBlocked) || !strings.Contains(err.Error(), "请稍后重试或切换更稳定的代理节点") {
			t.Fatalf("unconfirmed reachability %q must keep the generic hint: %v", reachability, err)
		}
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	checks.Store(0)
	ip, err := probeExitIPServices(context.Background(), &http.Client{Timeout: time.Second}, []exitProbeService{{id: "a", name: "a", endpoint: failing.URL}}, time.Minute, reachable.URL)
	if ip != "" || err == nil || errors.Is(err, ErrExitProbeBlocked) || checks.Load() != 0 {
		t.Fatalf("non-timeout failures must not run the reachability check: checks=%d err=%v", checks.Load(), err)
	}
}

func TestExitProbeServicesInvariants(t *testing.T) {
	if len(exitProbeServices) < 5 {
		t.Fatalf("expected diverse fallback exit probe services, got %d", len(exitProbeServices))
	}
	seenIDs := make(map[string]bool, len(exitProbeServices))
	seenHosts := make(map[string]bool, len(exitProbeServices))
	for _, service := range exitProbeServices {
		if service.id == "" || service.name == "" {
			t.Fatalf("exit probe service missing id or name: %+v", service)
		}
		if seenIDs[service.id] {
			t.Fatalf("duplicate exit probe service id: %q", service.id)
		}
		seenIDs[service.id] = true
		parsed, err := url.Parse(service.endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatalf("invalid exit probe endpoint %q: err=%v", service.endpoint, err)
		}
		if seenHosts[parsed.Host] {
			t.Fatalf("duplicate exit probe host: %q", parsed.Host)
		}
		seenHosts[parsed.Host] = true
	}
	// Worst-case all-timeout duration across all services must leave margin
	// inside the synthetic guest/browser diagnostic deadline so callers receive
	// a structured HTTP 503 rather than timing out with status 0.
	worstCase := time.Duration(len(exitProbeServices)-1)*exitProbeStagger + exitProbeRequestTimeout
	if worstCase >= diagnosticProbeTimeout {
		t.Fatalf("worst-case probe duration %v meets or exceeds diagnostic timeout %v", worstCase, diagnosticProbeTimeout)
	}
}
