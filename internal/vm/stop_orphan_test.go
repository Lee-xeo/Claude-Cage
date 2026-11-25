package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
)

func startFakeQMPServer(t *testing.T, sockPath string, handler func(cmd string) (reply string, extraEvents []string, afterReply func())) func() {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	if uln, ok := ln.(*net.UnixListener); ok {
		uln.SetUnlinkOnClose(false)
	}
	closeListener := func() { _ = ln.Close() }
	t.Cleanup(closeListener)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte(`{"QMP":{"version":{"qemu":{"major":11,"minor":1,"micro":0}},"capabilities":[]}}` + "\n"))
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req struct {
						Execute string `json:"execute"`
					}
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						return
					}
					reply, events, afterReply := handler(req.Execute)
					for _, ev := range events {
						_, _ = c.Write([]byte(ev + "\n"))
					}
					_, _ = c.Write([]byte(reply + "\n"))
					if afterReply != nil {
						_ = c.Close()
						afterReply()
						return
					}
				}
			}(conn)
		}
	}()
	return closeListener
}

func TestEvaluateWindowsStopPoll(t *testing.T) {
	cases := []struct {
		name     string
		status   limatype.Status
		haPID    int
		drvPID   int
		orphaned bool
		want     stopPollAction
	}{
		{"stopped", limatype.StatusStopped, 0, 0, false, stopPollStopped},
		{"running", limatype.StatusRunning, 100, 200, false, stopPollWait},
		{"broken_ha_alive_driver_exited", limatype.StatusBroken, 100, 0, false, stopPollWait},
		{"broken_orphaned_qemu", limatype.StatusBroken, 0, 200, true, stopPollStopOrphan},
		{"broken_ha_gone_non_orphan", limatype.StatusBroken, 0, 200, false, stopPollBrokenFail},
		{"broken_ha_gone_config_err", limatype.StatusBroken, 0, 0, false, stopPollBrokenFail},
	}
	for _, tc := range cases {
		if got := evaluateWindowsStopPoll(tc.status, tc.haPID, tc.drvPID, tc.orphaned); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSendQMPPowerdownHandlesAsyncEventsAndErrors(t *testing.T) {
	dir, err := os.MkdirTemp("", "qmp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, filenames.QMPSock)
	var sawPowerdown atomic.Bool
	closeOK := startFakeQMPServer(t, sock, func(cmd string) (string, []string, func()) {
		if cmd == "system_powerdown" {
			sawPowerdown.Store(true)
			return `{"return":{}}`, []string{`{"event":"POWERDOWN","timestamp":{"seconds":1,"microseconds":0}}`}, nil
		}
		return `{"return":{}}`, nil, nil
	})

	// Non-existent candidate first, valid socket second.
	if err := sendQMPPowerdown(context.Background(), []string{filepath.Join(dir, "missing.sock"), sock}); err != nil {
		t.Fatalf("sendQMPPowerdown failed: %v", err)
	}
	if !sawPowerdown.Load() {
		t.Fatal("QMP server never received system_powerdown")
	}
	closeOK()

	errSock := filepath.Join(dir, "err.sock")
	closeErr := startFakeQMPServer(t, errSock, func(cmd string) (string, []string, func()) {
		if cmd == "system_powerdown" {
			return `{"error":{"class":"GenericError","desc":"busy"}}`, nil, nil
		}
		return `{"return":{}}`, nil, nil
	})
	if err := sendQMPPowerdown(context.Background(), []string{errSock}); err == nil {
		t.Fatal("expected error when QMP returns error object")
	}
	closeErr()
}

func TestStopOrphanedWithHooksGracefulFallbackAndCleanup(t *testing.T) {
	savedWait := orphanGracefulWait
	orphanGracefulWait = 120 * time.Millisecond
	t.Cleanup(func() { orphanGracefulWait = savedWait })

	// 1. Graceful QMP shutdown closes the QMP listener before reporting process exit,
	// skips forceStop, and relies on cleanupInstanceTmpFiles to remove stale files.
	t.Run("graceful_qmp_exit", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "qmp-g-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		for _, f := range []string{"qemu.pid", "ha.pid", "ga.sock", "lima.yaml", "diffdisk"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var exited atomic.Bool
		sock := filepath.Join(dir, filenames.QMPSock)
		done := make(chan func(), 1)
		closeListener := startFakeQMPServer(t, sock, func(cmd string) (string, []string, func()) {
			if cmd == "system_powerdown" {
				return `{"return":{}}`, nil, func() {
					(<-done)()
					exited.Store(true)
				}
			}
			return `{"return":{}}`, nil, nil
		})
		done <- closeListener
		forceCalled := false
		err = stopOrphanedWithHooks(
			context.Background(),
			dir,
			[]string{sock},
			func(d time.Duration) (bool, error) {
				if exited.Load() {
					return true, nil
				}
				if d > 0 {
					time.Sleep(10 * time.Millisecond)
				}
				return exited.Load(), nil
			},
			func() error { forceCalled = true; return nil },
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if forceCalled {
			t.Fatal("forceStop called despite graceful QMP exit")
		}
		for _, gone := range []string{"qemu.pid", "ha.pid", "ga.sock", filenames.QMPSock} {
			if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
				t.Fatalf("stale tmp file %s was not cleaned up", gone)
			}
		}
		for _, kept := range []string{"lima.yaml", "diffdisk"} {
			if _, err := os.Lstat(filepath.Join(dir, kept)); err != nil {
				t.Fatalf("instance data file %s was unexpectedly removed: %v", kept, err)
			}
		}
	})

	// 2. When QMP succeeds but guest hangs, forceStop runs after grace window.
	t.Run("qmp_hang_falls_back_to_force", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "qmp-f-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		if err := os.WriteFile(filepath.Join(dir, "qemu.pid"), []byte("123"), 0o600); err != nil {
			t.Fatal(err)
		}
		sock := filepath.Join(dir, filenames.QMPSock)
		closeListener := startFakeQMPServer(t, sock, func(cmd string) (string, []string, func()) {
			return `{"return":{}}`, nil, nil
		})
		forceCalled := false
		err = stopOrphanedWithHooks(
			context.Background(),
			dir,
			[]string{sock},
			func(d time.Duration) (bool, error) {
				if d > 0 {
					time.Sleep(15 * time.Millisecond)
				}
				return false, nil
			},
			func() error {
				forceCalled = true
				closeListener()
				return nil
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !forceCalled {
			t.Fatal("forceStop was not invoked after QMP grace timeout")
		}
		for _, gone := range []string{"qemu.pid", filenames.QMPSock} {
			if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
				t.Fatalf("%s remained after forced stop", gone)
			}
		}
	})

	// 3. When QMP socket is absent, forceStop runs immediately and propagates errors.
	t.Run("missing_qmp_immediate_force", func(t *testing.T) {
		dir := t.TempDir()
		wantErr := errors.New("terminate failed")
		err := stopOrphanedWithHooks(
			context.Background(),
			dir,
			[]string{filepath.Join(dir, "missing.sock")},
			func(time.Duration) (bool, error) { return false, nil },
			func() error { return wantErr },
		)
		if !errors.Is(err, wantErr) {
			t.Fatalf("got %v, want %v", err, wantErr)
		}
	})
}

func TestPIDFileMatchesResolvedHandlesAliasesAndJunctions(t *testing.T) {
	realDir := `D:\Cage\CageData\state\lima\test`
	junctionDir := `C:\Users\alice\.claude-cage\state\lima\test`
	shortDir := `C:\Users\ALICE~1\.CLAUDE~1\state\lima\test`
	otherDir := `D:\Cage\CageData\state\lima\other`

	resolve := func(p string) (string, error) {
		switch p {
		case junctionDir, shortDir, realDir:
			return realDir, nil
		case junctionDir + `\qemu.pid`, shortDir + `\qemu.pid`, realDir + `\qemu.pid`:
			return realDir + `\qemu.pid`, nil
		case otherDir, otherDir + `\qemu.pid`:
			return p, nil
		default:
			return "", os.ErrNotExist
		}
	}

	if !pidFileMatchesResolved(realDir+`\qemu.pid`, realDir, resolve) {
		t.Fatal("exact path did not match")
	}
	if !pidFileMatchesResolved(`D:/Cage/CageData/state/lima/test/qemu.pid`, realDir, nil) {
		t.Fatal("forward-slash path did not match")
	}
	if !pidFileMatchesResolved(realDir+`\qemu.pid`, junctionDir, resolve) {
		t.Fatal("physical pidfile vs junction dir did not match")
	}
	if !pidFileMatchesResolved(shortDir+`\qemu.pid`, junctionDir, resolve) {
		t.Fatal("8.3 short pidfile vs junction dir did not match")
	}
	if pidFileMatchesResolved(otherDir+`\qemu.pid`, junctionDir, resolve) {
		t.Fatal("other instance pidfile falsely matched")
	}
	if pidFileMatchesResolved(realDir+`\ha.pid`, junctionDir, resolve) {
		t.Fatal("ha.pid falsely matched qemu.pid")
	}
}
