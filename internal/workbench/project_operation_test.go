package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func holdProjectNetworkMonitor(t *testing.T, s *Server) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	monitorStopped := make(chan struct{})
	go func() {
		defer close(monitorStopped)
		runNetworkMonitor(ctx, func(skipped *int) {
			s.withNetworkMonitorLock(s.startMonitorTiming(), skipped, func() {
				close(probeStarted)
				<-releaseProbe
			})
		})
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		close(releaseProbe)
		<-monitorStopped
	})
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		stop()
		t.Fatal("network monitor did not reach the held interface probe")
	}
	return stop
}

func TestProjectOperationsServeConcurrentWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, first, second := groupFixture(t)
		stopMonitor := holdProjectNetworkMonitor(t, s)
		defer stopMonitor()
		var requests []<-chan *httptest.ResponseRecorder
		for _, folder := range []string{first, second} {
			requests = append(requests, startProjectOperationRequest(t, s, context.Background(), "/api/projects/save", projectGroup{Name: "queued", Folders: []string{folder}}))
		}
		for _, done := range requests {
			assertProjectOperationWaiting(t, done)
		}
		stopMonitor()
		for _, done := range requests {
			if w := projectOperationResponse(t, done); w.Code != http.StatusOK {
				t.Fatalf("concurrent save: %d %s", w.Code, w.Body.String())
			}
		}
		groups, err := s.projectGroups()
		if err != nil || len(groups) != 2 {
			t.Fatalf("saved groups = %v, error = %v; want two distinct projects", groups, err)
		}
	})
}

func TestProjectDirectoryWaitPreservesAuthorization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		s.ready = true
		stopMonitor := holdProjectNetworkMonitor(t, s)
		defer stopMonitor()
		done := startProjectOperationRequest(t, s, context.Background(), "/api/input/files/directory", map[string]string{
			"target": "desktop", "id": testBrowserFileID, "workspace": t.TempDir(),
		})
		assertProjectOperationWaiting(t, done)
		stopMonitor()
		w := projectOperationResponse(t, done)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "请先通过打开项目") {
			t.Fatalf("directory authorization after waiting: %d %s", w.Code, w.Body.String())
		}
	})
}

func TestProjectOperationRetriesAfterMonitor(t *testing.T) {
	for _, scenario := range []string{"released", "timeout", "account locked", "request canceled", "closing", "job started"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testServer(t)
				s.ready = true
				s.op.Lock()
				unlock := sync.OnceFunc(s.op.Unlock)
				defer unlock()
				s.networkMonitorDone = make(chan struct{})
				ctx, cancel := context.WithCancel(context.WithValue(context.Background(), accountEpochKey{}, s.account.epoch))
				defer cancel()
				done := make(chan bool, 1)
				go func() {
					acquired := s.lockProjectOperation(ctx)
					if acquired {
						s.op.Unlock()
					}
					done <- acquired
				}()
				synctest.Wait()
				// Model the exact state after another operation wins the lock
				// released by the monitor, without a scheduler-dependent race.
				s.mu.Lock()
				close(s.networkMonitorDone)
				s.networkMonitorDone = nil
				s.mu.Unlock()
				time.Sleep(3 * projectOperationRetry)
				select {
				case acquired := <-done:
					t.Fatalf("gave up before the remaining deadline: %v", acquired)
				default:
				}
				switch scenario {
				case "released":
					unlock()
				case "timeout":
					time.Sleep(projectNetworkWait)
				case "account locked":
					s.account.lock()
				case "request canceled":
					cancel()
				case "closing":
					s.mu.Lock()
					s.closing = true
					s.mu.Unlock()
				case "job started":
					s.setJobForTest(Job{Running: true})
				}
				select {
				case acquired := <-done:
					if acquired != (scenario == "released") {
						t.Fatalf("acquired = %v for %s", acquired, scenario)
					}
				case <-time.After(time.Second):
					t.Fatal("wait did not finish")
				}
				s.mu.Lock()
				waiters := s.projectOperationWaiters
				s.mu.Unlock()
				if waiters != 0 {
					t.Fatalf("leaked waiters: %d", waiters)
				}
			})
		})
	}
}

func TestProjectOperationRejectsLifecycleContention(t *testing.T) {
	s := testServer(t)
	s.ready = true
	s.op.Lock()
	defer s.op.Unlock()
	if s.lockProjectOperation(context.Background()) {
		t.Fatal("waited for an unrelated operation without an active network monitor")
	}
}

func TestNetworkMonitorSeparatesProjectYieldFromLockSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		s.op.Lock()
		defer s.op.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			runNetworkMonitor(ctx, func(skipped *int) {
				timing := s.startMonitorTiming()
				s.withNetworkMonitorLock(timing, skipped, func() { panic("monitor acquired held lock") })
				s.recordMonitorTiming("network", timing)
			})
		}()
		synctest.Wait()
		// The log store rotates using filesystem mtimes (real time), while
		// synctest uses a virtual clock. Inspect each round before rotation.
		var records []map[string]string
		readRound := func() {
			current := monitorTimingRecords(t, s, "network")
			if len(current) == 0 {
				t.Fatal("missing monitor timing")
			}
			records = append(records, current[len(current)-1])
		}
		readRound()
		s.mu.Lock()
		s.projectOperationWaiters = 1
		s.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		readRound()
		s.mu.Lock()
		s.projectOperationWaiters = 0
		s.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		cancel()
		<-done
		readRound()
		if len(records) != 3 {
			t.Fatalf("monitor rounds = %v", records)
		}
		for _, i := range []int{0, 2} {
			if records[i]["result"] != "operation_lock_busy" || records[i]["consecutive_lock_skips"] != "1" {
				t.Fatalf("incorrect busy count: %v", records[i])
			}
		}
		if records[1]["result"] != "project_operations_waiting" || records[1]["consecutive_lock_skips"] != "" {
			t.Fatalf("project yield counted as contention: %v", records[1])
		}
	})
}

func startProjectOperationRequest(t *testing.T, s *Server, ctx context.Context, path string, input any) <-chan *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, s.origin+path, bytes.NewReader(body))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		done <- w
	}()
	return done
}

func assertProjectOperationWaiting(t *testing.T, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	synctest.Wait()
	select {
	case w := <-done:
		t.Fatalf("request did not wait for the monitor: %d %s", w.Code, w.Body.String())
	default:
	}
}

func projectOperationResponse(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(time.Second):
		t.Fatal("project request did not finish after releasing the monitor or canceling")
		return nil
	}
}

func TestProjectOperationsWaitForNetworkMonitor(t *testing.T) {
	for _, operation := range []string{"save", "open"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, folder, _ := groupFixture(t)
				group := projectGroup{Name: "monitor-project", Folders: []string{folder}}
				var body any = group
				if operation == "open" {
					saveGroupRequest(t, s, group, http.StatusOK)
					body = map[string]any{"workspace": folder, "add": false, "activate": true}
				}
				stopMonitor := holdProjectNetworkMonitor(t, s)
				defer stopMonitor()
				done := startProjectOperationRequest(t, s, context.Background(), "/api/projects/"+operation, body)
				assertProjectOperationWaiting(t, done)
				stopMonitor()
				w := projectOperationResponse(t, done)
				if w.Code != http.StatusOK {
					t.Fatalf("%s after monitor released lock: %d %s", operation, w.Code, w.Body.String())
				}
				groups, err := s.projectGroups()
				if err != nil || len(groups) != 1 || groups[0].Workspace != folder {
					t.Fatalf("saved projects = %+v, error = %v; want one project for %q", groups, err, folder)
				}
				if operation == "open" && s.workspace() != folder {
					t.Fatalf("project was not activated: %q", s.workspace())
				}
			})
		})
	}
}

func TestProjectSaveAbandonsNetworkWait(t *testing.T) {
	for _, scenario := range []struct {
		name string
		stop func(*Server, context.CancelFunc)
	}{
		{"request canceled", func(_ *Server, cancel context.CancelFunc) { cancel() }},
		{"account locked", func(s *Server, _ context.CancelFunc) { s.account.lock() }},
		{"timeout", func(_ *Server, _ context.CancelFunc) { time.Sleep(projectNetworkWait) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, folder, _ := groupFixture(t)
				stopMonitor := holdProjectNetworkMonitor(t, s)
				defer stopMonitor()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := startProjectOperationRequest(t, s, ctx, "/api/projects/save", projectGroup{
					Name: "monitor-canceled-project", Folders: []string{folder},
				})
				assertProjectOperationWaiting(t, done)
				scenario.stop(s, cancel)
				w := projectOperationResponse(t, done)
				if w.Code != http.StatusConflict {
					t.Fatalf("abandoned save returned %d: %s", w.Code, w.Body.String())
				}
				stopMonitor()
				synctest.Wait()
				if _, err := os.Stat(filepath.Join(s.profile, "projects.json")); !os.IsNotExist(err) {
					t.Fatalf("abandoned save changed project records: %v", err)
				}
			})
		})
	}
}
