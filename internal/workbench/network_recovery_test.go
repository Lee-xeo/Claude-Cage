package workbench

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestExitProbeTimeoutKeepsExistingLeaseWithoutRenewing(t *testing.T) {
	s, gate := grantedToolUpdateServer(t)
	s.setJobForTest(Job{})
	s.safety.VerifiedAt = time.Now().Add(-time.Minute)
	before := gate.Status()
	verified := s.safety.VerifiedAt
	for i := 0; i < 2; i++ {
		s.monitorSafetyWithProbe(func(context.Context, cage.Config) (string, error) {
			return "", context.DeadlineExceeded
		})
		after := gate.Status()
		if after.Mode != cage.GateOpen || after.Until != before.Until || !s.safety.VerifiedAt.Equal(verified) {
			t.Fatalf("timeout changed the existing lease: before=%+v after=%+v", before, after)
		}
	}
	client, err := s.gateClient()
	if err != nil {
		t.Fatal(err)
	}
	// Windows can grant and renew within the same wall-clock tick.
	// Advance past the grant tick before requiring a later deadline.
	for !time.Now().Add(cage.GateLease).After(before.Until) {
		time.Sleep(time.Millisecond)
	}
	s.finishSafetyMonitor(context.Background(), client, gate.Status(), "8.8.8.8", "")
	if !gate.Status().Until.After(before.Until) || s.safety.monitorError != "" {
		t.Fatalf("successful observation did not renew and clear the retry notice: before=%+v after=%+v monitorError=%q", before, gate.Status(), s.safety.monitorError)
	}
	s.safety.VerifiedAt = time.Now().Add(-time.Minute)
	gate.Close("检查或监测授权已到期")
	s.monitorSafetyWithProbe(func(context.Context, cage.Config) (string, error) {
		t.Fatal("expired gate must not probe or reopen")
		return "", nil
	})
	if gate.Status().Mode != cage.GateClosed || s.recovery.RunID == "" {
		t.Fatal("expired lease did not remain closed and schedule recovery")
	}
}

func TestMonitorLocationFailureSchedulesRecoveryButViolationDoesNot(t *testing.T) {
	for _, policy := range []cage.ExitPolicy{
		{Mode: "static", IP: "8.8.8.8"},
		{Mode: "dynamic", Scope: "any"},
		{Mode: "dynamic", Scope: "country", CountryCode: "US"},
	} {
		for _, condition := range []string{"location unavailable", "outside country", "mainland location", "mainland range", "user paused"} {
			if condition == "outside country" && (policy.Mode == "static" || policy.Scope == "any") {
				continue
			}
			t.Run(policy.Mode+"/"+policy.Scope+"/"+condition, func(t *testing.T) {
				s := testServer(t)
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodConnect {
						t.Error("location bypassed proxy tunnel")
					}
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				defer proxy.Close()
				cfg, _ := loadConfig(s.configPath)
				cfg.ProxyURL = proxy.URL
				cfg.ExitPolicy = policy
				if err := writePrivate(s.configPath, cfg); err != nil {
					t.Fatal(err)
				}
				gate, err := cage.NewEgressGate(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(s.instanceDir(), 0700); err != nil {
					t.Fatal(err)
				}
				stop, err := cage.StartGateControl(s.configPath, s.instanceDir(), gate)
				if err != nil {
					t.Fatal(err)
				}
				defer stop()
				if err := gate.BeginCheck(); err != nil {
					t.Fatal(err)
				}
				state := gate.Status()
				report := cage.SafetyReport{Policy: cage.SafetyPolicyVersion, ID: "location-monitor", RunID: state.RunID, CheckID: state.CheckID, Configuration: state.Configuration, CreatedAt: time.Now()}
				for _, id := range []string{"egress_policy", "proxy_transport", "dns_transport", "guest_settings", "browser_settings", "filesystem_boundary", "management_boundary", "fail_closed", "udp_policy", "ipv6_policy", "exit_consistency"} {
					report.Checks = append(report.Checks, cage.SafetyCheck{ID: id, Title: id, Critical: true, State: cage.CheckPassed, Evidence: "isolated fixture", CheckedAt: report.CreatedAt})
				}
				if err := gate.Grant(report); err != nil {
					t.Fatal(err)
				}
				s.ready = true
				s.safety.VerifiedAt = time.Now().Add(-time.Minute)
				if condition == "mainland location" {
					s.locations = map[string]Location{"8.8.8.8": {IP: "8.8.8.8", CountryCode: "CN", CheckedAt: time.Now()}}
				}
				if condition == "outside country" {
					s.locations = map[string]Location{"8.8.8.8": {IP: "8.8.8.8", CountryCode: "JP", CheckedAt: time.Now()}}
				}
				if condition == "user paused" {
					if err := s.pauseNetworkNow("user pause"); err != nil {
						t.Fatal(err)
					}
				}
				s.monitorSafetyWithProbe(func(context.Context, cage.Config) (string, error) {
					if condition == "user paused" {
						t.Fatal("monitor probed while paused")
					}
					if condition == "mainland range" {
						return "223.5.5.5", nil
					}
					return "8.8.8.8", nil
				})
				if gate.Status().Mode != cage.GateClosed {
					t.Fatal("unverified or paused network stayed open")
				}
				if (s.recovery.RunID != "") != (condition == "location unavailable") {
					t.Fatalf("incorrect recovery admission for %s: %+v", condition, s.recovery)
				}
			})
		}
	}
}

func TestNetworkRecoveryAdmissionRequiresObservedPressureToDefer(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		status  cage.ResourceStatus
		wantJob bool
	}{
		{"memory target could not be applied", cage.ResourceStatus{ObservedAt: now, Error: "内存目标未能应用，保持当前配置"}, true},
		{"failed sample retains stale guest pressure", cage.ResourceStatus{ObservedAt: now.Add(-time.Minute), Error: "资源采样暂时不可用，已暂停内存回收", Pressure: true}, true},
		{"current guest pressure", cage.ResourceStatus{ObservedAt: now, Pressure: true}, false},
		{"current host pressure", cage.ResourceStatus{HostPressure: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, gate := failedMonitoringServer(t)
			s.recovery.Next = time.Now().Add(-time.Second)
			tc.status.Version, tc.status.UpdatedAt = 1, time.Now()
			if err := writePrivate(filepath.Join(s.instanceDir(), "cage-resources.json"), tc.status); err != nil {
				t.Fatal(err)
			}
			// Inspect actual job admission before allowing the queued worker to run.
			s.op.Lock()
			s.recoverNetwork()
			job := s.jobs.snapshot()
			s.jobs.cancelRunning()
			s.op.Unlock()
			s.jobs.close()
			if (job.Running && job.Kind == "network-recovery") != tc.wantJob || gate.Status().Mode != cage.GateClosed {
				t.Fatalf("job = %+v, want admitted = %v while gate remains closed", job, tc.wantJob)
			}
		})
	}
}

func failedMonitoringServer(t *testing.T) (*Server, *cage.EgressGate) {
	s, gate := grantedToolUpdateServer(t)
	s.setJobForTest(Job{})
	client, err := s.gateClient()
	if err != nil {
		t.Fatal(err)
	}
	s.finishSafetyObservation(context.Background(), client, gate.Status(), "", "guest observation timed out", true)
	if gate.Status().Mode != cage.GateClosed || s.recovery.RunID == "" {
		t.Fatal("temporary failure did not revoke and schedule")
	}
	return s, gate
}

func TestNetworkRecoveryDoesNotReusePriorGrant(t *testing.T) {
	s, gate := failedMonitoringServer(t)
	expected := s.recovery
	called := false
	err := s.runNetworkRecovery(expected, func() error {
		called = true
		if gate.Status().Mode != cage.GateClosed {
			t.Fatal("opened before full check")
		}
		return errors.New("new full check failed")
	})
	if !called || err == nil || gate.Status().Mode != cage.GateClosed || s.recovery.Attempts != 1 || !s.recovery.Next.After(time.Now().Add(20*time.Second)) {
		t.Fatal("failed check did not remain closed with backoff", err, s.recovery)
	}
}

func TestNetworkRecoveryHonorsInterveningChanges(t *testing.T) {
	for _, change := range []string{"pause", "configuration", "new-check", "stop"} {
		t.Run(change, func(t *testing.T) {
			s, gate := failedMonitoringServer(t)
			expected := s.recovery
			switch change {
			case "pause":
				if err := s.pauseNetworkNow("user pause"); err != nil {
					t.Fatal(err)
				}
			case "configuration":
				cfg, _ := loadConfig(s.configPath)
				cfg.Timezone = "Asia/Tokyo"
				if err := writePrivate(s.configPath, cfg); err != nil {
					t.Fatal(err)
				}
			case "new-check":
				if err := gate.BeginCheck(); err != nil {
					t.Fatal(err)
				}
			case "stop":
				s.closing = true
			}
			err := s.runNetworkRecovery(expected, func() error { t.Fatal("stale recovery ran"); return nil })
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if s.recovery.RunID != "" {
				t.Fatal("stale recovery retained")
			}
		})
	}
}

func TestObsoleteNetworkRecoveryLeavesNewerRetryScheduled(t *testing.T) {
	for _, change := range []string{"reclosed", "resumed"} {
		t.Run(change, func(t *testing.T) {
			s, gate := failedMonitoringServer(t)
			expected := s.recovery
			switch change {
			case "reclosed":
				gate.Close("运行监测未能续期")
			case "resumed":
				s.mu.Lock()
				s.safety.pauseVersion++
				s.mu.Unlock()
			}
			s.scheduleNetworkRecovery(gate.Status())
			newer := s.recovery
			if newer.RunID == "" || newer == expected {
				t.Fatal("newer retry was not scheduled", newer)
			}
			err := s.runNetworkRecovery(expected, func() error { t.Fatal("obsolete recovery ran"); return nil })
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if s.recovery != newer {
				t.Fatal("obsolete recovery dropped the newer retry", s.recovery)
			}
		})
	}
}

func TestNetworkRecoverySuccessfulFullCheckClearsRetry(t *testing.T) {
	s, gate := failedMonitoringServer(t)
	expected := s.recovery
	err := s.runNetworkRecovery(expected, func() error {
		if err := gate.BeginCheck(); err != nil {
			return err
		}
		state := gate.Status()
		report := *state.Report
		report.CheckID = state.CheckID
		report.ID = "fresh-recovery"
		report.CreatedAt = time.Now()
		for i := range report.Checks {
			report.Checks[i].CheckedAt = report.CreatedAt
		}
		return gate.Grant(report)
	})
	if err != nil || gate.Status().Mode != cage.GateOpen || s.recovery.RunID != "" {
		t.Fatal(err, gate.Status().Mode, s.recovery)
	}
}

func TestConfigurationMismatchDoesNotScheduleRecovery(t *testing.T) {
	s, gate := grantedToolUpdateServer(t)
	s.setJobForTest(Job{})
	state := gate.Status()
	cfg, _ := loadConfig(s.configPath)
	cfg.Timezone = "Asia/Tokyo"
	if err := writePrivate(s.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	client, _ := s.gateClient()
	s.finishSafetyObservation(context.Background(), client, state, "", "guest observation timed out", true)
	if s.recovery.RunID != "" || gate.Status().Mode != cage.GateClosed {
		t.Fatal("configuration change got transient recovery")
	}
}

func appliedProxyHealthServer(t *testing.T) (*Server, *cage.EgressGate) {
	t.Helper()
	s, gate := grantedToolUpdateServer(t)
	s.setJobForTest(Job{})
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	p.Sources = []managedproxy.Source{{
		ID:   "src-1",
		Name: "Fixture",
		Nodes: []managedproxy.Node{{
			ID:     "node-1",
			Name:   "Exit",
			Type:   "socks5",
			Config: map[string]any{"name": "node-1", "type": "socks5", "server": "203.0.113.10", "port": 1080},
		}},
	}}
	p.Selected = "node-1"
	p.Revision = "rev-1"
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = p.Endpoint()
	cfg.ProxyRevision = p.Revision
	if err := writePrivate(s.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	// Replace the gate with one matching the applied proxy configuration.
	newGate, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := cage.StartGateControl(s.configPath, s.instanceDir(), newGate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if err := newGate.BeginCheck(); err != nil {
		t.Fatal(err)
	}
	grantTestGate(t, newGate)
	s.publishGate(newGate.Status())
	_ = gate
	return s, newGate
}

func TestProxyHealthRecoverySchedulesFullRecheckAfterCoreRecovers(t *testing.T) {
	s, gate := appliedProxyHealthServer(t)
	unhealthy := func(context.Context, string, managedproxy.Store) string { return "readiness.configs.timeout" }
	healthy := func(context.Context, string, managedproxy.Store) string { return "" }

	// Step 1: Transient proxy health failure closes the gate without scheduling recovery yet.
	s.checkProxyHealthWith(unhealthy)
	closed := gate.Status()
	if closed.Mode != cage.GateClosed || closed.Reason != proxyNotReadyReason {
		t.Fatalf("expected closed gate with proxy reason, got %+v", closed)
	}
	if s.recovery.RunID != "" {
		t.Fatalf("recovery scheduled while proxy core is still unhealthy: %+v", s.recovery)
	}

	// Step 2: Core recovers -> schedules full recheck (15s wait) while keeping gate closed.
	s.checkProxyHealthWith(healthy)
	if gate.Status().Mode != cage.GateClosed {
		t.Fatalf("healthy proxy reopened gate directly without full recheck: %+v", gate.Status())
	}
	first := s.recovery
	if first.RunID != closed.RunID || first.Epoch != closed.Epoch || first.Configuration != closed.Configuration || first.Attempts != 0 {
		t.Fatalf("unexpected scheduled recovery: %+v (gate=%+v)", first, closed)
	}
	if !first.Next.After(time.Now().Add(10 * time.Second)) {
		t.Fatalf("recovery did not preserve 15s wait: %+v", first)
	}

	// Step 3: Repeated healthy ticks do not overwrite the existing schedule or backoff.
	s.mu.Lock()
	s.recovery.Attempts = 2
	s.recovery.Next = time.Now().Add(45 * time.Second)
	backedOff := s.recovery
	s.mu.Unlock()
	s.checkProxyHealthWith(healthy)
	if s.recovery != backedOff {
		t.Fatalf("repeated healthy tick reset backoff: got %+v, want %+v", s.recovery, backedOff)
	}

	// Step 4: Another proxy failure while waiting cancels pending recovery until healthy again.
	s.checkProxyHealthWith(unhealthy)
	if s.recovery.RunID != "" || gate.Status().Mode != cage.GateClosed {
		t.Fatalf("second proxy failure did not clear pending recovery: recovery=%+v gate=%+v", s.recovery, gate.Status())
	}

	// Step 5: Core recovers again and full check passes -> gate opens and recovery clears.
	s.checkProxyHealthWith(healthy)
	expected := s.recovery
	if expected.RunID == "" || expected.Epoch != gate.Status().Epoch {
		t.Fatalf("recovery not rescheduled for new closed epoch: %+v (gate=%+v)", expected, gate.Status())
	}
	err := s.runNetworkRecovery(expected, func() error {
		if err := gate.BeginCheck(); err != nil {
			return err
		}
		state := gate.Status()
		report := *state.Report
		report.CheckID = state.CheckID
		report.ID = "proxy-health-recovered"
		report.CreatedAt = time.Now()
		for i := range report.Checks {
			report.Checks[i].CheckedAt = report.CreatedAt
		}
		return gate.Grant(report)
	})
	if err != nil || gate.Status().Mode != cage.GateOpen || s.recovery.RunID != "" {
		t.Fatalf("full recovery failed: err=%v gate=%+v recovery=%+v", err, gate.Status(), s.recovery)
	}
}

func TestProxyHealthRecoveryRespectsPauseConfigReportAndReason(t *testing.T) {
	unhealthy := func(context.Context, string, managedproxy.Store) string { return "readiness.configs" }
	healthy := func(context.Context, string, managedproxy.Store) string { return "" }

	for _, tc := range []string{"manual-pause", "config-changed", "other-close-reason", "no-prior-report"} {
		t.Run(tc, func(t *testing.T) {
			s, gate := appliedProxyHealthServer(t)
			switch tc {
			case "manual-pause":
				s.checkProxyHealthWith(unhealthy)
				if err := s.pauseNetworkNow("用户暂停外网"); err != nil {
					t.Fatal(err)
				}
			case "config-changed":
				s.checkProxyHealthWith(unhealthy)
				cfg, _ := loadConfig(s.configPath)
				cfg.Timezone = "Asia/Tokyo"
				if err := writePrivate(s.configPath, cfg); err != nil {
					t.Fatal(err)
				}
			case "other-close-reason":
				gate.Close("出口位于不允许的地区")
				s.publishGate(gate.Status())
			case "no-prior-report":
				cfg, _ := loadConfig(s.configPath)
				freshGate, err := cage.NewEgressGate(cfg)
				if err != nil {
					t.Fatal(err)
				}
				stop, err := cage.StartGateControl(s.configPath, s.instanceDir(), freshGate)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(stop)
				freshGate.Close(proxyNotReadyReason)
				s.publishGate(freshGate.Status())
				gate = freshGate
			}
			s.checkProxyHealthWith(healthy)
			if s.recovery.RunID != "" {
				t.Fatalf("%s unexpectedly scheduled recovery: %+v", tc, s.recovery)
			}
			if gate.Status().Mode != cage.GateClosed {
				t.Fatalf("%s unexpectedly opened gate: %+v", tc, gate.Status())
			}
		})
	}
}
