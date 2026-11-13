package workbench

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

func TestScreenSharingIsolationError(t *testing.T) {
	if err := screenSharingIsolationError(cage.GateStatus{Mode: cage.GateClosed}, nil); err != nil {
		t.Fatalf("closed gate should be isolated: %v", err)
	}
	for _, mode := range []string{cage.GateOpen, cage.GateChecking, "", "unknown"} {
		if !errors.Is(screenSharingIsolationError(cage.GateStatus{Mode: mode}, nil), errScreenSharingNotIsolated) {
			t.Errorf("mode %q should be refused", mode)
		}
	}
	if !errors.Is(screenSharingIsolationError(cage.GateStatus{Mode: cage.GateClosed}, errors.New("offline")), errScreenSharingGateUnknown) {
		t.Fatal("a gate read failure must be refused")
	}
}

func TestSetupSharingSatisfied(t *testing.T) {
	usable := macOSScreenSharingStatus{Usable: true, Authorized: cage.CheckUnknown}
	if setupSharingSatisfied(usable, false, false) {
		t.Fatal("usable but unverified authorization should still need the settings toggle")
	}
	if !setupSharingSatisfied(usable, true, false) || !setupSharingSatisfied(usable, false, true) {
		t.Fatal("opening or confirming the settings pane should complete first setup")
	}
	authorized := macOSScreenSharingStatus{Usable: true, Authorized: cage.CheckPassed}
	if !setupSharingSatisfied(authorized, false, false) {
		t.Fatal("an authorized usable session should pass without a repeated toggle")
	}
	denied := macOSScreenSharingStatus{Usable: false, Authorized: cage.CheckFailed}
	if setupSharingSatisfied(denied, true, true) {
		t.Fatal("a denied handshake must not be treated as complete")
	}
}

func TestAssembleScreenSharingStatus(t *testing.T) {
	denied := assembleScreenSharingStatus(true, true, nil, screenSharingRFBResult{
		Greeting:  "RFB 003.008",
		Handshake: true,
		Denied:    true,
		Reason:    "Screen Sharing is not allowed on localhost",
	}, nil)
	if denied.Usable || denied.State != cage.CheckFailed || denied.Authorized != cage.CheckFailed {
		t.Fatalf("denied handshake: %+v", denied)
	}
	if !strings.Contains(denied.Summary, "系统尚未允许") {
		t.Fatalf("denied summary: %s", denied.Summary)
	}

	ok := assembleScreenSharingStatus(true, true, nil, screenSharingRFBResult{
		Greeting:  "RFB 003.008",
		Handshake: true,
		Types:     2,
	}, nil)
	if !ok.Usable || ok.State != cage.CheckPassed || ok.Authorized != cage.CheckUnknown {
		t.Fatalf("successful handshake: %+v", ok)
	}
	if !strings.Contains(ok.Summary, "RFB 握手成功") || !strings.Contains(ok.Summary, "不被允许") {
		t.Fatalf("successful summary should still guide the toggle: %s", ok.Summary)
	}

	down := assembleScreenSharingStatus(false, false, nil, screenSharingRFBResult{Unreachable: true}, errScreenSharingUnreachable)
	if down.Usable || down.State != cage.CheckFailed {
		t.Fatalf("service down: %+v", down)
	}
}

func TestReadScreenSharingRFB(t *testing.T) {
	t.Run("security types", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		go func() {
			defer server.Close()
			_, _ = server.Write([]byte("RFB 003.008\n"))
			var echoed [12]byte
			if _, err := io.ReadFull(server, echoed[:]); err != nil {
				return
			}
			_, _ = server.Write([]byte{2, 30, 33})
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := readScreenSharingRFB(ctx, client)
		if err != nil || !result.Handshake || result.Denied || result.Types != 2 || result.Greeting != "RFB 003.008" {
			t.Fatalf("types: %+v err=%v", result, err)
		}
	})
	t.Run("not allowed reason", func(t *testing.T) {
		reason := "屏幕共享在 localhost 上不被允许"
		client, server := net.Pipe()
		defer client.Close()
		go func() {
			defer server.Close()
			_, _ = server.Write([]byte("RFB 003.008\n"))
			var echoed [12]byte
			_, _ = io.ReadFull(server, echoed[:])
			payload := append([]byte{0}, 0, 0, 0, 0)
			binary.BigEndian.PutUint32(payload[1:], uint32(len(reason)))
			_, _ = server.Write(append(payload, []byte(reason)...))
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := readScreenSharingRFB(ctx, client)
		if err != nil || !result.Handshake || !result.Denied || result.Reason != reason {
			t.Fatalf("denied: %+v err=%v", result, err)
		}
		if !screenSharingAuthorizationDenied(result.Reason) {
			t.Fatal("chinese localhost refusal was not recognized")
		}
	})
	t.Run("hang up after version", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		go func() {
			_, _ = server.Write([]byte("RFB 003.008\n"))
			var echoed [12]byte
			_, _ = io.ReadFull(server, echoed[:])
			server.Close()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		result, err := readScreenSharingRFB(ctx, client)
		if err != nil || !result.Handshake || !result.Denied || !strings.Contains(result.Reason, "系统设置") {
			t.Fatalf("hang up: %+v err=%v", result, err)
		}
	})
}

func TestOpenMacOSScreenSharingActions(t *testing.T) {
	s := testServer(t)
	writeScreenSharingSSHConfig(t, s, 55413)

	if w := screenSharingRequest(s, true, "{}"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errScreenSharingNotMacOS.Error()) {
		t.Fatalf("without a macOS guest: %d %s", w.Code, w.Body.String())
	}

	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "blue"}
	if w := screenSharingRequest(s, true, `{"action":"open"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errScreenSharingNotReady.Error()) {
		t.Fatalf("not ready: %d %s", w.Code, w.Body.String())
	}

	s.ready = true
	var enabled int
	s.screenSharing = &macOSScreenSharing{
		gate: func(context.Context) (cage.GateStatus, error) {
			return cage.GateStatus{Mode: cage.GateOpen}, nil
		},
		enable: func(context.Context) error {
			enabled++
			return nil
		},
		inspect: func(context.Context) (macOSScreenSharingStatus, error) {
			return assembleScreenSharingStatus(true, true, nil, screenSharingRFBResult{Greeting: "RFB 003.008", Handshake: true, Types: 1}, nil), nil
		},
		open: func(string) error { return nil },
		startSSH: func(ctx context.Context, args []string) *exec.Cmd {
			return startRFBStub(t, ctx, forwardPort(t, args))
		},
	}

	if w := screenSharingRequest(s, true, `{"action":"enable"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errScreenSharingNotIsolated.Error()) {
		t.Fatalf("enable with open gate: %d %s", w.Code, w.Body.String())
	}
	if enabled != 0 {
		t.Fatal("enable ran while the guest was not isolated")
	}

	s.screenSharing.gate = func(context.Context) (cage.GateStatus, error) {
		return cage.GateStatus{Mode: cage.GateChecking}, nil
	}
	if w := screenSharingRequest(s, true, `{"action":"enable"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errScreenSharingNotIsolated.Error()) {
		t.Fatalf("enable while checking: %d %s", w.Code, w.Body.String())
	}

	s.screenSharing.gate = func(context.Context) (cage.GateStatus, error) {
		return cage.GateStatus{}, errors.New("missing")
	}
	if w := screenSharingRequest(s, true, `{"action":"enable"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errScreenSharingGateUnknown.Error()) {
		t.Fatalf("enable with unknown gate: %d %s", w.Code, w.Body.String())
	}

	if w := screenSharingRequest(s, false, `{"action":"check"}`); w.Code != http.StatusForbidden {
		t.Fatalf("untrusted view: %d %s", w.Code, w.Body.String())
	}

	// Daily connect and status checks do not require the gate to be closed.
	s.screenSharing.gate = func(context.Context) (cage.GateStatus, error) {
		return cage.GateStatus{Mode: cage.GateOpen}, nil
	}
	if w := screenSharingRequest(s, true, `{"action":"open"}`); w.Code != http.StatusOK {
		t.Fatalf("daily open with gate open: %d %s", w.Code, w.Body.String())
	}
	if w := screenSharingRequest(s, true, `{"action":"check"}`); w.Code != http.StatusOK {
		t.Fatalf("status check with gate open: %d %s", w.Code, w.Body.String())
	}
	var status macOSScreenSharingStatus
	if err := json.Unmarshal(screenSharingRequest(s, true, `{"action":"check"}`).Body.Bytes(), &status); err != nil || !status.Usable || status.State != cage.CheckPassed {
		t.Fatalf("status payload: %+v", status)
	}

	s.screenSharing.gate = func(context.Context) (cage.GateStatus, error) {
		return cage.GateStatus{Mode: cage.GateClosed}, nil
	}
	w := screenSharingRequest(s, true, `{"action":"enable"}`)
	if w.Code != http.StatusOK || enabled != 1 {
		t.Fatalf("isolated enable: %d %s enabled=%d", w.Code, w.Body.String(), enabled)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || !strings.Contains(status.Summary, "已重新启用") || !status.Usable {
		t.Fatalf("enable payload: %s", w.Body.String())
	}

	if w := screenSharingRequest(s, true, `{"action":"repair"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errScreenSharingBadAction.Error()) {
		t.Fatalf("unknown action: %d %s", w.Code, w.Body.String())
	}
}

func TestOpenMacOSScreenSharingTunnel(t *testing.T) {
	s := testServer(t)
	writeScreenSharingSSHConfig(t, s, 58716)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "blue"}
	s.ready = true
	var opened []string
	var sshStarts int
	s.screenSharing = &macOSScreenSharing{
		gate: func(context.Context) (cage.GateStatus, error) {
			return cage.GateStatus{Mode: cage.GateClosed}, nil
		},
		open: func(url string) error {
			opened = append(opened, url)
			return nil
		},
		startSSH: func(ctx context.Context, args []string) *exec.Cmd {
			sshStarts++
			if !containsArg(args, "58716") || !containsArg(args, "blue") || !containsArg(args, "cage-macos") {
				t.Errorf("unexpected ssh args: %q", args)
			}
			if containsArg(args, "ClearAllForwardings=yes") {
				t.Error("forward command cleared local forwards")
			}
			port := forwardPort(t, args)
			return startRFBStub(t, ctx, port)
		},
	}

	w := screenSharingRequest(s, true, `{"action":"open"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("isolated open: %d %s", w.Code, w.Body.String())
	}
	var reply struct {
		OK      bool   `json:"ok"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.OK || !strings.HasPrefix(reply.Address, "127.0.0.1:") {
		t.Fatalf("reply: %s", w.Body.String())
	}
	if len(opened) != 1 || opened[0] != "vnc://"+reply.Address {
		t.Fatalf("open url: %q", opened)
	}

	w = screenSharingRequest(s, true, `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reuse: %d %s", w.Code, w.Body.String())
	}
	if sshStarts != 1 {
		t.Fatalf("tunnel was recreated: starts=%d", sshStarts)
	}
	if len(opened) != 2 {
		t.Fatalf("reuse should reopen the client: %q", opened)
	}

	s.publishGate(cage.GateStatus{Mode: cage.GateOpen, RunID: "run", Epoch: 1})
	s.screenSharing.mu.Lock()
	alive := screenSharingProcessAlive(s.screenSharing.cmd)
	port := s.screenSharing.port
	s.screenSharing.mu.Unlock()
	if !alive || port == 0 {
		t.Fatal("opening the gate tore down a daily-use tunnel")
	}

	s.screenSharing.gate = func(context.Context) (cage.GateStatus, error) {
		return cage.GateStatus{Mode: cage.GateOpen}, nil
	}
	w = screenSharingRequest(s, true, `{"action":"open"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("open after grant: %d %s", w.Code, w.Body.String())
	}
	if sshStarts != 1 {
		t.Fatalf("grant recreated the tunnel: starts=%d", sshStarts)
	}

	s.closeMacOSScreenSharing()
	s.screenSharing.startSSH = func(ctx context.Context, args []string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	w = screenSharingRequest(s, true, `{"action":"open"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errScreenSharingUnreachable.Error()) {
		t.Fatalf("unreachable guest: %d %s", w.Code, w.Body.String())
	}
}

func TestMacOSSharingMaintenanceRequiresIsolation(t *testing.T) {
	s := testServer(t)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "blue"}
	s.ready = true
	s.screenSharing = &macOSScreenSharing{gate: func(context.Context) (cage.GateStatus, error) {
		return cage.GateStatus{Mode: cage.GateOpen}, nil
	}}
	r := httptest.NewRequest(http.MethodPost, s.origin+"/api/macos/maintenance", strings.NewReader(`{"action":"open","maintenance":"sharing"}`))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), errScreenSharingNotIsolated.Error()) {
		t.Fatalf("sharing maintenance with open gate: %d %s", w.Code, w.Body.String())
	}
}

func TestCloseMacOSScreenSharingOnStop(t *testing.T) {
	s := testServer(t)
	writeScreenSharingSSHConfig(t, s, 55413)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "blue"}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s.screenSharing = &macOSScreenSharing{cmd: cmd, cancel: cancel, port: ln.Addr().(*net.TCPAddr).Port}
	s.closeMacOSScreenSharing()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not tear down the ssh child")
	}
	if cmd.ProcessState == nil {
		t.Fatal("stopped process was not reaped")
	}
}

func screenSharingRequest(s *Server, trusted bool, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, s.origin+"/api/environment/macos-screen-sharing", strings.NewReader(body))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	if trusted {
		r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}

func writeScreenSharingSSHConfig(t *testing.T, s *Server, port int) {
	t.Helper()
	dir := filepath.Join(s.profile, "state", "lima", "mac")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := "Host lima-mac\n User cageprovision\n Port " + strconv.Itoa(port) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh.config"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func forwardPort(t *testing.T, args []string) int {
	t.Helper()
	for i, arg := range args {
		if arg != "-L" || i+1 >= len(args) {
			continue
		}
		// 127.0.0.1:PORT:127.0.0.1:5900
		parts := strings.Split(args[i+1], ":")
		if len(parts) < 4 {
			t.Fatalf("forward %q", args[i+1])
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		return port
	}
	t.Fatalf("missing -L in %q", args)
	return 0
}

func startRFBStub(t *testing.T, ctx context.Context, port int) *exec.Cmd {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("RFB 003.008\n"))
			var echoed [12]byte
			_, _ = io.ReadFull(conn, echoed[:])
			_, _ = conn.Write([]byte{1, 30})
			_, _ = io.Copy(io.Discard, conn)
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	// The API starts this process; hook only prepares the listening stub.
	return exec.CommandContext(ctx, "sleep", "30")
}
