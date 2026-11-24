package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func interfaceMonitorServer(t *testing.T) (*Server, *cage.EgressGate) {
	t.Helper()
	s := testServer(t)
	if _, err := guestprofile.Create(s.profile, guestprofile.Options{}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	cfg, _ := loadConfig(s.configPath)
	p, _ := managedproxy.Load(s.profile)
	p.Port = listener.Addr().(*net.TCPAddr).Port
	if err = p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = p.Endpoint()
	if err := writePrivate(s.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.MkdirAll(filepath.Join(s.profile, "proxy"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("interface-name: en0\nmixed-port: " + strconv.Itoa(port) + "\n")
	if err := os.WriteFile(filepath.Join(s.profile, "proxy", "config.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.profile, "state", "lima", cage.InstanceName(s.profile, s.environmentDir()))
	if err := writePrivate(filepath.Join(dir, "cage-running.json"), map[string]int{"pid": os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	gate, err := cage.NewEgressGate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := cage.StartGateControl(s.configPath, dir, gate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if err := gate.BeginCheck(); err != nil {
		t.Fatal(err)
	}
	return s, gate
}

func TestInterfaceRecoveryRevokesGateAndPreservesManualPause(t *testing.T) {
	previous := physicalProxyInterface
	t.Cleanup(func() { physicalProxyInterface = previous })
	for _, tc := range []struct {
		name, next   string
		paused, lost bool
	}{
		{"Wi-Fi to Ethernet", "en7", false, false},
		{"Ethernet while paused", "en7", true, false},
		{"same interface after outage", "en0", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, gate := interfaceMonitorServer(t)
			s.networkPaused, s.proxyNetworkLost = tc.paused, tc.lost
			physicalProxyInterface = func(context.Context) (string, error) { return tc.next, nil }
			observed := s.observeProxyInterface(context.Background())
			// Keep the recovery queued: assert the synchronous revocation before
			// any core can restart, then cancel through the normal stop path.
			s.op.Lock()
			s.applyProxyInterfaceObservation(observed)
			s.mu.Lock()
			queued, paused := s.jobs.snapshot().Kind == "proxy-network" && s.jobs.snapshot().Running, s.networkPaused
			s.mu.Unlock()
			closed := gate.Status().Mode == cage.GateClosed
			finished := make(chan struct{})
			s.launch(httptest.NewRecorder(), "stop", func() error { close(finished); return nil })
			s.op.Unlock()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("stop could not cancel network recovery")
			}
			s.op.Lock()
			s.op.Unlock()
			if !queued || !closed || paused != tc.paused {
				t.Fatalf("queued=%v closed=%v paused=%v; wanted pause=%v", queued, closed, paused, tc.paused)
			}
		})
	}
}

func TestMissingPhysicalInterfaceClosesGateWithoutUnboundFallback(t *testing.T) {
	s, gate := interfaceMonitorServer(t)
	grantTestGate(t, gate)
	before := gate.Status()
	destination := netip.MustParseAddrPort("8.8.8.8:443")
	if before.Mode != cage.GateOpen || !gate.Allows("tcp", destination) {
		t.Fatal("fixture did not grant public egress before the network failure")
	}
	previous := physicalProxyInterface
	t.Cleanup(func() { physicalProxyInterface = previous })
	const disconnected = "physical network unavailable"
	physicalProxyInterface = func(context.Context) (string, error) { return "", errors.New(disconnected) }
	observed := s.observeProxyInterface(context.Background())
	s.op.Lock()
	s.applyProxyInterfaceObservation(observed)
	s.op.Unlock()
	after := gate.Status()
	if after.Mode != cage.GateClosed || after.Reason != disconnected || after.Epoch <= before.Epoch ||
		gate.Allows("tcp", destination) || s.networkPaused || !s.proxyNetworkLost || s.jobs.snapshot().Running {
		t.Fatalf("physical network failure did not revoke public egress: %+v", after)
	}
	data, _ := os.ReadFile(filepath.Join(s.profile, "proxy", "config.yaml"))
	if !strings.Contains(string(data), "interface-name: en0") {
		t.Fatal("missing uplink removed the existing binding")
	}
}

func TestInterfaceRecoveryRetainsFailureForRetry(t *testing.T) {
	s, gate := interfaceMonitorServer(t)
	physical, validate := physicalProxyInterface, validateProxyCore
	t.Cleanup(func() { physicalProxyInterface, validateProxyCore = physical, validate })
	physicalProxyInterface = func(context.Context) (string, error) { return "en0", nil }
	validateProxyCore = func(context.Context, string, managedproxy.Store) error {
		return errors.New("代理配置验证失败")
	}
	s.proxyNetworkLost = true
	s.safety.monitorError = "old interface unavailable"
	s.applyProxyInterfaceObservation(s.observeProxyInterface(context.Background()))
	failed := awaitJobIdle(t, &s.jobs)
	s.op.Lock()
	if failed.Error == "" {
		t.Fatal("rebind failure did not finish", failed)
	}
	s.mu.Lock()
	if !s.proxyNetworkLost || !strings.Contains(s.safety.monitorError, "代理配置验证失败") || gate.Status().Mode != cage.GateClosed {
		t.Fatal("failed rebind lost its pending state or retained a stale error", s.proxyNetworkLost, s.safety.monitorError)
	}
	s.mu.Unlock()
	s.applyProxyInterfaceObservation(s.observeProxyInterface(context.Background()))
	if s.jobs.snapshot().ID != failed.ID {
		t.Fatal("recovery retried without waiting")
	}
	s.mu.Lock()
	s.proxyNetworkRetry = time.Now().Add(-time.Second)
	s.mu.Unlock()
	s.applyProxyInterfaceObservation(s.observeProxyInterface(context.Background()))
	s.op.Unlock()
	retried := awaitJobIdle(t, &s.jobs)
	if retried.ID == failed.ID || retried.Error == "" {
		t.Fatal("same interface was not retried after the failure", retried)
	}
	s.op.Lock()
	defer s.op.Unlock()
	path := filepath.Join(s.instanceDir(), "cage-running.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.proxyNetworkRetry = time.Time{}
	s.applyProxyInterfaceObservation(s.observeProxyInterface(context.Background()))
	if s.jobs.snapshot().ID != retried.ID {
		t.Fatal("stopped environment queued another rebind")
	}
}

func TestInterfaceRecoveryClearsPendingOnlyAfterSuccessfulApply(t *testing.T) {
	s, gate := interfaceMonitorServer(t)
	physical, validate, start := physicalProxyInterface, validateProxyCore, startProxyCore
	t.Cleanup(func() { physicalProxyInterface, validateProxyCore, startProxyCore = physical, validate, start })
	physicalProxyInterface = func(context.Context) (string, error) { return "en0", nil }
	validateProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	startProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	before := gate.Status()
	s.networkPaused, s.proxyNetworkLost = true, true
	s.safety.monitorError = "物理网卡暂时不可用"
	s.applyProxyInterfaceObservation(s.observeProxyInterface(context.Background()))
	job := awaitJobIdle(t, &s.jobs)
	s.op.Lock()
	defer s.op.Unlock()
	if job.Error != "" || s.proxyNetworkLost || !s.proxyNetworkRetry.IsZero() || s.safety.monitorError != "" {
		t.Fatal("successful rebind left outage pending", job)
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil || gate.Status().Configuration != cfg.Fingerprint() || gate.Status().RunID != before.RunID || !s.vmRunning() {
		t.Fatal("rebind did not preserve the running environment", err)
	}
	if !s.networkPaused || gate.Status().Mode != cage.GateClosed {
		t.Fatal("rebind overrode the manual pause")
	}
}

func TestPhysicalInterfaceObservationIsDiscardedAfterNetworkChange(t *testing.T) {
	previous := physicalProxyInterface
	physicalProxyInterface = func(context.Context) (string, error) { return "", errors.New("old physical network unavailable") }
	t.Cleanup(func() { physicalProxyInterface = previous })
	for _, changed := range []string{"configuration", "job"} {
		t.Run(changed, func(t *testing.T) {
			s, gate := interfaceMonitorServer(t)
			grantTestGate(t, gate)
			observed := s.observeProxyInterface(context.Background())
			if changed == "configuration" {
				cfg, err := loadConfig(s.configPath)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Timezone = "Etc/GMT+1"
				if err := writePrivate(s.configPath, cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				s.setJobForTest(Job{ID: "completed-during-probe"})
			}
			s.op.Lock()
			s.applyProxyInterfaceObservation(observed)
			s.op.Unlock()
			if gate.Status().Mode != cage.GateOpen || s.proxyNetworkLost || s.safety.monitorError != "" {
				t.Fatal("a stale interface result revoked the current network state")
			}
		})
	}
}

// markTestVMRunning records this test process as the local VM's owner.
func markTestVMRunning(t *testing.T, s *Server) {
	t.Helper()
	if err := writePrivate(filepath.Join(s.instanceDir(), "cage-running.json"), map[string]int{"pid": os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if !s.vmRunning() {
		t.Fatal("test VM record was not accepted")
	}
}

// A stopped local VM sends no traffic through the proxy, and starting it
// checks and rebinds the interface; the monitor does not read host routes.
func TestStoppedVMSkipsPhysicalInterfaceProbe(t *testing.T) {
	s := testServer(t)
	previous := physicalProxyInterface
	probes := 0
	physicalProxyInterface = func(context.Context) (string, error) { probes++; return "en0", nil }
	defer func() { physicalProxyInterface = previous }()
	if observed := s.observeProxyInterface(context.Background()); probes != 0 || observed.configuration != "" {
		t.Fatalf("stopped VM probed the host route: probes=%d observed=%+v", probes, observed)
	}
	markTestVMRunning(t, s)
	if s.observeProxyInterface(context.Background()); probes != 1 {
		t.Fatalf("running VM was not observed: probes=%d", probes)
	}
}

func TestNodeRouteInterfaceDriftRevokesGateAndRebinds(t *testing.T) {
	prevPhysical, prevMatches := physicalProxyInterface, proxyBindingMatches
	t.Cleanup(func() {
		physicalProxyInterface = prevPhysical
		proxyBindingMatches = prevMatches
	})
	s, gate := interfaceMonitorServer(t)
	grantTestGate(t, gate)
	// Physical interface stays en0, while the per-node route interface (e.g.
	// Tailscale) has drifted out of sync with config.yaml.
	physicalProxyInterface = func(context.Context) (string, error) { return "en0", nil }
	proxyBindingMatches = func(string, string) bool { return false }

	observed := s.observeProxyInterface(context.Background())
	s.op.Lock()
	s.applyProxyInterfaceObservation(observed)
	s.mu.Lock()
	queued := s.jobs.snapshot().Kind == "proxy-network" && s.jobs.snapshot().Running
	s.mu.Unlock()
	status := gate.Status()
	finished := make(chan struct{})
	s.launch(httptest.NewRecorder(), "stop", func() error { close(finished); return nil })
	s.op.Unlock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stop could not cancel route-interface recovery")
	}
	s.op.Lock()
	s.op.Unlock()
	if !queued || status.Mode != cage.GateClosed || !strings.Contains(status.Reason, "路由接口") {
		t.Fatalf("queued=%v gate=%+v", queued, status)
	}
}

func TestStoppedVMDeadInterfaceAutoRebindsRunningCore(t *testing.T) {
	s, _ := interfaceMonitorServer(t)
	if err := os.Remove(filepath.Join(s.instanceDir(), "cage-running.json")); err != nil {
		t.Fatal(err)
	}
	prevPhysical, prevAvailable, prevRunning, prevValidate, prevStart :=
		physicalProxyInterface, proxyInterfaceAvailable, proxyInterfaceCoreRunning, validateProxyCore, startProxyCore
	t.Cleanup(func() {
		physicalProxyInterface, proxyInterfaceAvailable, proxyInterfaceCoreRunning, validateProxyCore, startProxyCore =
			prevPhysical, prevAvailable, prevRunning, prevValidate, prevStart
	})
	probes := 0
	physicalProxyInterface = func(context.Context) (string, error) { probes++; return "wlp4s0", nil }
	proxyInterfaceAvailable = func(name string) bool { return name == "en0" }
	proxyInterfaceCoreRunning = func(context.Context, string, managedproxy.Store) bool { return true }
	validateProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	rebound := false
	startProxyCore = func(_ context.Context, profile string, _ managedproxy.Store) error {
		rebound = true
		return os.WriteFile(filepath.Join(profile, "proxy", "config.yaml"), []byte("interface-name: wlp4s0\n"), 0600)
	}

	// While en0 is still available, idle stopped-VM ticks skip physical probing.
	if observed := s.observeProxyInterface(context.Background()); probes != 0 || observed.configuration != "" {
		t.Fatalf("healthy stopped VM probed host route: probes=%d observed=%+v", probes, observed)
	}

	// Once en0 disappears while the core is running, the monitor detects the
	// replacement uplink and automatically rebinds the core.
	proxyInterfaceAvailable = func(string) bool { return false }
	observed := s.observeProxyInterface(context.Background())
	if probes != 1 || !observed.stoppedCoreDeadInterface || observed.name != "wlp4s0" {
		t.Fatalf("dead interface not observed: probes=%d observed=%+v", probes, observed)
	}
	s.op.Lock()
	s.applyProxyInterfaceObservation(observed)
	s.op.Unlock()
	job := awaitJobIdle(t, &s.jobs)
	if job.Error != "" || !rebound {
		t.Fatalf("stopped-VM dead interface did not rebind: rebound=%v job=%+v", rebound, job)
	}
	if bound, err := managedproxy.BoundInterface(filepath.Join(s.profile, "proxy", "config.yaml")); err != nil || bound != "wlp4s0" {
		t.Fatalf("bound interface after rebind = %q (%v), want wlp4s0", bound, err)
	}
}

func TestEnsureProxyInterfaceBeforeUseRebindsStoppedAndReportsRunningVM(t *testing.T) {
	s, _ := interfaceMonitorServer(t)
	prevPhysical, prevRunning, prevStart := physicalProxyInterface, proxyInterfaceCoreRunning, startProxyCore
	t.Cleanup(func() {
		physicalProxyInterface, proxyInterfaceCoreRunning, startProxyCore = prevPhysical, prevRunning, prevStart
	})
	physicalProxyInterface = func(context.Context) (string, error) { return "wlp4s0", nil }
	proxyInterfaceCoreRunning = func(context.Context, string, managedproxy.Store) bool { return true }
	restarts := 0
	startProxyCore = func(_ context.Context, profile string, _ managedproxy.Store) error {
		restarts++
		return os.WriteFile(filepath.Join(profile, "proxy", "config.yaml"), []byte("interface-name: wlp4s0\n"), 0600)
	}

	// While VM is running, do not restart behind the gate; return StaleInterfaceError.
	err := s.ensureProxyInterfaceBeforeUse(context.Background())
	var stale *managedproxy.StaleInterfaceError
	if !errors.As(err, &stale) || restarts != 0 {
		t.Fatalf("running VM got err=%v restarts=%d, want StaleInterfaceError and 0 restarts", err, restarts)
	}

	// /api/proxy/logs surfaces the stale-interface diagnostic clearly.
	w := request(s, "GET", "/api/proxy/logs", s.origin, "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "en0") || !strings.Contains(w.Body.String(), "wlp4s0") {
		t.Fatalf("proxy logs missing stale-interface warning: %d %s", w.Code, w.Body.String())
	}

	// With VM stopped, explicit probe/delay use automatically rebinds the core.
	if err := os.Remove(filepath.Join(s.instanceDir(), "cage-running.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureProxyInterfaceBeforeUse(context.Background()); err != nil || restarts != 1 {
		t.Fatalf("stopped VM rebind failed: err=%v restarts=%d", err, restarts)
	}
	// Now bound to wlp4s0, subsequent checks are a fast-path no-op.
	if err := s.ensureProxyInterfaceBeforeUse(context.Background()); err != nil || restarts != 1 {
		t.Fatalf("already-rebound check restarted again: err=%v restarts=%d", err, restarts)
	}
}

func TestSettingsProbeRebindsActiveCoreOnlyWithoutBootstrapPreflight(t *testing.T) {
	s, _ := interfaceMonitorServer(t)
	if err := os.Remove(filepath.Join(s.instanceDir(), "cage-running.json")); err != nil {
		t.Fatal(err)
	}
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Import("src", "", []byte(`proxies: [{name: node-a, type: socks5, server: node.example.com, port: 1080}]`), "src", 0, ""); err != nil {
		t.Fatal(err)
	}
	p.Selected = p.Sources[0].Nodes[0].ID
	p.Revision = managedproxy.Token()
	p.BootstrapDNS = []string{"https://1.1.1.1/dns-query"}
	p.GuestDNS = []string{"https://1.1.1.1/dns-query"}
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = p.Endpoint()
	cfg.ProxyRevision = p.Revision
	cfg.DNS = cage.NewDNSOptions(nil, []string{"https://1.1.1.1/dns-query"}, false)
	if err := writePrivate(s.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := cage.SaveDNSOptions(s.configPath, cfg.DNS); err != nil {
		t.Fatal(err)
	}

	prevPhysical, prevRunning, prevStart := physicalProxyInterface, proxyInterfaceCoreRunning, startProxyCore
	prevPreflight, prevProbe := startPreflightProxy, probeEnvironmentLocation
	t.Cleanup(func() {
		physicalProxyInterface, proxyInterfaceCoreRunning, startProxyCore = prevPhysical, prevRunning, prevStart
		startPreflightProxy, probeEnvironmentLocation = prevPreflight, prevProbe
	})

	const preflightEndpoint = "socks5://cage:preflight@127.0.0.1:19877"
	restarts := 0
	preflights := 0
	physicalProxyInterface = func(context.Context) (string, error) { return "wlp4s0", nil }
	proxyInterfaceCoreRunning = func(context.Context, string, managedproxy.Store) bool { return true }
	startProxyCore = func(_ context.Context, profile string, _ managedproxy.Store) error {
		restarts++
		return os.WriteFile(filepath.Join(profile, "proxy", "config.yaml"), []byte("interface-name: wlp4s0\n"), 0600)
	}
	startPreflightProxy = func(context.Context, string, managedproxy.Store) (*managedproxy.PreflightProxy, error) {
		preflights++
		return &managedproxy.PreflightProxy{Endpoint: preflightEndpoint}, nil
	}
	probeEnvironmentLocation = func(_ context.Context, c cage.Config, _ Preferences) (Location, error) {
		if c.ProxyURL == preflightEndpoint {
			return Location{IP: "8.8.8.8", CountryCode: "US", CheckedAt: time.Now()}, nil
		}
		return Location{IP: "1.1.1.1", CountryCode: "US", CheckedAt: time.Now()}, nil
	}

	// Candidate bootstrap DNS probe uses isolated preflight core without restarting the active core.
	candidateView := settingsView(cfg, s.preferences)
	candidateView.DNSBootstrapURLs = []string{"https://223.6.6.6/dns-query"}
	body, _ := json.Marshal(candidateView)
	w := request(s, "POST", "/api/settings/probe", s.origin, string(body), true)
	if w.Code != 200 || preflights != 1 || restarts != 0 {
		t.Fatalf("candidate preflight probe got code=%d preflights=%d restarts=%d body=%s", w.Code, preflights, restarts, w.Body.String())
	}

	// Ordinary probe using the active core auto-rebinds the stale stopped-VM interface first.
	ordinaryView := settingsView(cfg, s.preferences)
	body, _ = json.Marshal(ordinaryView)
	w = request(s, "POST", "/api/settings/probe", s.origin, string(body), true)
	if w.Code != 200 || preflights != 1 || restarts != 1 {
		t.Fatalf("ordinary probe got code=%d preflights=%d restarts=%d body=%s", w.Code, preflights, restarts, w.Body.String())
	}
}
