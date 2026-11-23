package workbench

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAccountNetworkBridgeAuthAndRelay(t *testing.T) {
	dir, err := os.MkdirTemp("", "anb-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "n.sock")
	unixLn, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer unixLn.Close()

	var upstreamHits atomic.Int32
	var receivedHeader atomic.Value
	go func() {
		for {
			c, err := unixLn.Accept()
			if err != nil {
				return
			}
			upstreamHits.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				var hdr strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					hdr.WriteString(line)
					if strings.TrimRight(line, "\r\n") == "" {
						break
					}
				}
				receivedHeader.Store(hdr.String())
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				buf := make([]byte, 32)
				n, _ := r.Read(buf)
				_, _ = c.Write([]byte("ECHO:" + string(buf[:n])))
			}(c)
		}
	}()

	bridge, err := startAccountNetworkBridge(sock)
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	defer bridge.close()

	// 1. Unauthenticated request must be rejected with 403 and never reach upstream.
	unauth, err := net.Dial("tcp", bridge.addr())
	if err != nil {
		t.Fatalf("dial unauth: %v", err)
	}
	_, _ = io.WriteString(unauth, "CONNECT 1.1.1.1:443 HTTP/1.1\r\nHost: 1.1.1.1:443\r\n\r\n")
	resp, _ := io.ReadAll(unauth)
	_ = unauth.Close()
	if !strings.HasPrefix(string(resp), "HTTP/1.1 403 ") {
		t.Fatalf("unauthenticated response = %q, want 403", string(resp))
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("unauthenticated client reached upstream (%d hits)", upstreamHits.Load())
	}

	// 2. Wrong token must also be rejected without hitting upstream.
	wrong, err := net.Dial("tcp", bridge.addr())
	if err != nil {
		t.Fatalf("dial wrong: %v", err)
	}
	_, _ = io.WriteString(wrong, "CONNECT 1.1.1.1:443 HTTP/1.1\r\nHost: 1.1.1.1:443\r\nProxy-Authorization: Bearer wrong\r\n\r\n")
	resp, _ = io.ReadAll(wrong)
	_ = wrong.Close()
	if !strings.HasPrefix(string(resp), "HTTP/1.1 403 ") {
		t.Fatalf("wrong token response = %q, want 403", string(resp))
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("wrong-token client reached upstream (%d hits)", upstreamHits.Load())
	}

	// 3. Valid token succeeds, strips Proxy-Authorization, and relays full duplex.
	auth, err := net.Dial("tcp", bridge.addr())
	if err != nil {
		t.Fatalf("dial auth: %v", err)
	}
	defer auth.Close()
	_, _ = io.WriteString(auth, "CONNECT 1.1.1.1:443 HTTP/1.1\r\nHost: 1.1.1.1:443\r\nProxy-Authorization: Bearer "+bridge.token+"\r\n\r\n")
	r := bufio.NewReader(auth)
	statusLine, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(statusLine, "HTTP/1.1 200 ") {
		t.Fatalf("authenticated status=%q err=%v", statusLine, err)
	}
	_, _ = r.ReadString('\n')
	_, _ = io.WriteString(auth, "hello")
	reply := make([]byte, 32)
	n, err := r.Read(reply)
	if err != nil || string(reply[:n]) != "ECHO:hello" {
		t.Fatalf("relay reply=%q err=%v, want ECHO:hello", string(reply[:n]), err)
	}
	hdr, _ := receivedHeader.Load().(string)
	if strings.Contains(strings.ToLower(hdr), "proxy-authorization") {
		t.Fatalf("Proxy-Authorization header leaked to upstream: %q", hdr)
	}
}

func TestRunClaudeRefreshPreservesAccountRouteError(t *testing.T) {
	s := testServer(t)
	routeErr := &accountRouteError{
		message: "账号专属网络启动失败，已阻止回落到环境出口（SSH 通道退出码 255）",
		cause:   errors.New("exit status 255"),
	}
	s.claudeAccounts.probe = func(context.Context, string) (claudeGuestSnapshot, error) {
		return claudeGuestSnapshot{}, routeErr
	}
	call := s.claudeAccounts.claim("0123456789abcdef01234567")
	s.runClaudeRefresh(context.Background(), "0123456789abcdef01234567", "0123456789abcdef01234567", call, false)
	if !errors.Is(call.err, errAccountRouteFailed) {
		t.Fatalf("got err=%v, want errAccountRouteFailed", call.err)
	}
	if !strings.Contains(call.err.Error(), "SSH 通道退出码 255") {
		t.Fatalf("lost underlying route error details: %v", call.err)
	}
}
