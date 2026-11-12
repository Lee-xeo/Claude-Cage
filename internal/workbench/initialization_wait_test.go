package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

// These tests use the real middleware, handler, and private record writer, but
// only isolated profiles and an in-memory ready guest. The synctest clock moves
// through actual 6s/10s lock holds and the 15s timeout without wall-clock sleeps.
func initializationWaitServer(t *testing.T) *Server {
	t.Helper()
	s := testServer(t)
	if w := initializationRequest(s, "install", true); w.Code != http.StatusOK {
		t.Fatalf("prepare tracked initialization: %d %s", w.Code, w.Body.String())
	}
	s.windowsGuest = &guestos.WindowsProfile{}
	s.windowsState.Running = true
	s.ready = true
	cfg, err := cage.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	report := cage.SafetyReport{Policy: cage.SafetyPolicyVersion, ID: "initialization-wait", RunID: "fixture-vm", CheckID: "fixture-check", Configuration: cfg.Fingerprint(), CreatedAt: time.Now()}
	for _, id := range []string{"egress_policy", "proxy_transport", "dns_transport", "guest_settings", "browser_settings", "filesystem_boundary", "management_boundary", "fail_closed", "udp_policy", "ipv6_policy", "exit_consistency"} {
		report.Checks = append(report.Checks, cage.SafetyCheck{ID: id, Critical: true, State: cage.CheckPassed, Evidence: "isolated test", CheckedAt: report.CreatedAt})
	}
	s.safety.Gate = &cage.GateStatus{Mode: cage.GateOpen, PolicyVerified: true, RunID: report.RunID, Configuration: cfg.Fingerprint(), Until: time.Now().Add(time.Minute), Report: &report}
	return s
}

func initializationWaitHTTPRequest(s *Server, ctx context.Context, action string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, s.origin+"/api/environment/initialization", strings.NewReader(`{"action":"`+action+`"}`)).WithContext(ctx)
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	return r
}

func initializationWaitStart(s *Server, r *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		done <- w
	}()
	return done
}

func initializationWaitPending(t *testing.T, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	synctest.Wait()
	select {
	case w := <-done:
		t.Fatalf("completion did not wait for the operation lock: %d %s", w.Code, w.Body.String())
	default:
	}
}

func initializationWaitResult(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(time.Second):
		t.Fatal("completion did not finish within one second after release/cancellation")
		return nil
	}
}

func initializationWaitRecord(t *testing.T, s *Server) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.profile, "initialization.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func initializationWaitUnchanged(t *testing.T, s *Server, before []byte) {
	t.Helper()
	if after := initializationWaitRecord(t, s); !bytes.Equal(after, before) {
		t.Fatalf("rejected completion changed initialization record: before=%s after=%s", before, after)
	}
}

func TestInitializationCompletionWaitsForSlowOperationAndPersists(t *testing.T) {
	for _, hold := range []time.Duration{6 * time.Second, 10 * time.Second} {
		t.Run(hold.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := initializationWaitServer(t)
				s.op.Lock()
				locked := true
				defer func() {
					if locked {
						s.op.Unlock()
					}
				}()
				started := time.Now()
				done := initializationWaitStart(s, initializationWaitHTTPRequest(s, context.Background(), "complete"))
				initializationWaitPending(t, done)
				time.Sleep(hold)
				initializationWaitPending(t, done)
				s.op.Unlock()
				locked = false
				w := initializationWaitResult(t, done)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"completed":true`) {
					t.Fatalf("completion after %s hold: %d %s", hold, w.Code, w.Body.String())
				}
				if elapsed := time.Since(started); elapsed < hold || elapsed > hold+time.Second {
					t.Fatalf("unexpected completion elapsed time: %s", elapsed)
				}
				var record initializationRecord
				if err := json.Unmarshal(initializationWaitRecord(t, s), &record); err != nil || !record.Completed {
					t.Fatalf("missing durable completion: %+v %v", record, err)
				}
				// A new controller reads the same durable record, without relying
				// on the previous in-memory ready or running state.
				restarted := testServer(t)
				restarted.profile = s.profile
				w = initializationRequest(restarted, "", true)
				var view initializationView
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Required || !view.Tracked {
					t.Fatalf("restart lost completed initialization: %d %s", w.Code, w.Body.String())
				}
			})
		})
	}
}

func TestInitializationBusyActionsRejectWithoutWaiting(t *testing.T) {
	for _, action := range []string{"begin", "install", "complete"} {
		for _, operationLocked := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/busy-job", true: "/held-lock"}[operationLocked], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := initializationWaitServer(t)
					before := initializationWaitRecord(t, s)
					if operationLocked {
						s.op.Lock()
						defer s.op.Unlock()
					}
					if !operationLocked || action == "complete" {
						s.setJobForTest(Job{Running: true, Kind: "start"})
					}
					started := time.Now()
					w := initializationRequest(s, action, true)
					if w.Code != http.StatusConflict || time.Since(started) != 0 {
						t.Fatalf("busy %s waited or was accepted: elapsed=%s status=%d body=%s", action, time.Since(started), w.Code, w.Body.String())
					}
					initializationWaitUnchanged(t, s, before)
				})
			})
		}
	}
}

func TestInitializationCompletionTimesOutWithoutLateWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := initializationWaitServer(t)
		before := initializationWaitRecord(t, s)
		s.op.Lock()
		locked := true
		defer func() {
			if locked {
				s.op.Unlock()
			}
		}()
		started := time.Now()
		done := initializationWaitStart(s, initializationWaitHTTPRequest(s, context.Background(), "complete"))
		initializationWaitPending(t, done)
		time.Sleep(15*time.Second - time.Nanosecond)
		initializationWaitPending(t, done)
		time.Sleep(time.Nanosecond)
		w := initializationWaitResult(t, done)
		if w.Code != http.StatusConflict || time.Since(started) != 15*time.Second {
			t.Fatalf("timeout must be bounded at 15 seconds: elapsed=%s status=%d body=%s", time.Since(started), w.Code, w.Body.String())
		}
		initializationWaitUnchanged(t, s, before)
		s.op.Unlock()
		locked = false
		time.Sleep(20 * time.Second)
		synctest.Wait()
		initializationWaitUnchanged(t, s, before)
		if !s.op.TryLock() {
			t.Fatal("timed-out request left a detached operation-lock holder")
		}
		s.op.Unlock()
	})
}

func TestInitializationCompletionCancellationDoesNotWrite(t *testing.T) {
	for _, reason := range []string{"request", "account-lock", "account-lock-and-unlock", "service-close"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := initializationWaitServer(t)
				before := initializationWaitRecord(t, s)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s.op.Lock()
				locked := true
				defer func() {
					if locked {
						s.op.Unlock()
					}
				}()
				done := initializationWaitStart(s, initializationWaitHTTPRequest(s, ctx, "complete"))
				initializationWaitPending(t, done)
				time.Sleep(6 * time.Second)
				initializationWaitPending(t, done)
				switch reason {
				case "request":
					cancel()
				case "account-lock", "account-lock-and-unlock":
					s.account.lock()
					if reason == "account-lock-and-unlock" {
						s.account.mu.Lock()
						s.account.setKey(make([]byte, 32))
						s.account.mu.Unlock()
					}
				case "service-close":
					s.closeBackground()
				}
				w := initializationWaitResult(t, done)
				if w.Code < 400 {
					t.Fatalf("%s accepted a canceled completion: %d %s", reason, w.Code, w.Body.String())
				}
				initializationWaitUnchanged(t, s, before)
				s.op.Unlock()
				locked = false
				time.Sleep(20 * time.Second)
				synctest.Wait()
				initializationWaitUnchanged(t, s, before)
			})
		})
	}
}

func TestInitializationCompletionRechecksStateAfterWait(t *testing.T) {
	for _, change := range []string{"guest-stopped", "not-ready", "gate-closed", "gate-expired", "config-changed", "storage-recovery", "not-local", "trusted-proof-changed", "job-started"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := initializationWaitServer(t)
				before := initializationWaitRecord(t, s)
				if change == "gate-expired" {
					s.safety.Gate.Until = time.Now().Add(5 * time.Second)
				}
				s.op.Lock()
				locked := true
				defer func() {
					if locked {
						s.op.Unlock()
					}
				}()
				done := initializationWaitStart(s, initializationWaitHTTPRequest(s, context.Background(), "complete"))
				initializationWaitPending(t, done)
				time.Sleep(6 * time.Second)
				initializationWaitPending(t, done)
				s.mu.Lock()
				switch change {
				case "guest-stopped":
					s.windowsState.Running = false
				case "not-ready":
					s.ready = false
				case "gate-closed":
					s.safety.Gate.Mode = cage.GateClosed
				case "storage-recovery":
					s.storageRecoveryError = "isolated recovery failure"
				case "not-local":
					s.clientOnly = true
				case "trusted-proof-changed":
					s.token = strings.Repeat("c", 64)
				}
				s.mu.Unlock()
				if change == "config-changed" {
					cfg, err := cage.Load(s.configPath)
					if err != nil {
						t.Fatal(err)
					}
					cfg.Timezone = "Asia/Shanghai"
					if err := writePrivate(s.configPath, cfg); err != nil {
						t.Fatal(err)
					}
				}
				if change == "job-started" {
					s.setJobForTest(Job{Running: true, Kind: "start"})
				}
				s.op.Unlock()
				locked = false
				w := initializationWaitResult(t, done)
				if w.Code < 400 {
					t.Fatalf("%s was not revalidated: %d %s", change, w.Code, w.Body.String())
				}
				initializationWaitUnchanged(t, s, before)
			})
		})
	}
}

// Blocking JSON intake separates middleware admission from handler lock
// acquisition. A new unlock must not lend its epoch to an old admitted request.
type initializationWaitBody struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *initializationWaitBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.reader.Read(p)
}

func TestInitializationCompletionRetainsMiddlewareAccountEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := initializationWaitServer(t)
		before := initializationWaitRecord(t, s)
		body := &initializationWaitBody{reader: strings.NewReader(`{"action":"complete"}`), entered: make(chan struct{}), release: make(chan struct{})}
		r := initializationWaitHTTPRequest(s, context.Background(), "complete")
		r.Body = io.NopCloser(body)
		done := initializationWaitStart(s, r)
		<-body.entered
		synctest.Wait()
		s.account.lock()
		s.account.mu.Lock()
		s.account.setKey(make([]byte, 32))
		s.account.mu.Unlock()
		close(body.release)
		w := initializationWaitResult(t, done)
		if w.Code < 400 {
			t.Fatalf("old admission borrowed a new account epoch: %d %s", w.Code, w.Body.String())
		}
		initializationWaitUnchanged(t, s, before)
		if fresh := initializationRequest(s, "complete", true); fresh.Code != http.StatusOK {
			t.Fatalf("newly admitted request could not complete: %d %s", fresh.Code, fresh.Body.String())
		}
	})
}
