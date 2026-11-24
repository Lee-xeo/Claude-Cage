package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
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

func TestAddressChangeOnSameInterfaceRevokesGateAndRebinds(t *testing.T) {
	prevPhysical, prevMatches, prevAddrChanged := physicalProxyInterface, proxyBindingMatches, proxyBindingAddressChanged
	t.Cleanup(func() {
		physicalProxyInterface = prevPhysical
		proxyBindingMatches = prevMatches
		proxyBindingAddressChanged = prevAddrChanged
	})
	s, gate := interfaceMonitorServer(t)
	grantTestGate(t, gate)
	physicalProxyInterface = func(context.Context) (string, error) { return "en0", nil }
	proxyBindingMatches = func(string, string) bool { return false }
	proxyBindingAddressChanged = func(string, string) bool { return true }

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
		t.Fatal("stop could not cancel address-change recovery")
	}
	s.op.Lock()
	s.op.Unlock()
	if !queued || status.Mode != cage.GateClosed || !strings.Contains(status.Reason, "地址或状态已变化") {
		t.Fatalf("queued=%v gate=%+v", queued, status)
	}
}

func TestStoppedProxyAutoRebindsOnProbeAndDelay(t *testing.T) {
	s := testServer(t)
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := p.Selected
	configPath := filepath.Join(managedproxy.Dir(s.profile), "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("interface-name: en0\nmixed-port: 17896\n"), 0600); err != nil {
		t.Fatal(err)
	}

	prevPhysical := physicalProxyInterface
	prevMatches := proxyBindingMatches
	prevValidate := validateProxyCore
	prevStart := startProxyCore
	prevDelay := measureProxyDelay
	prevRunning := proxyCoreRunning
	prevCheck := checkProxyRuntimeBinding
	prevHint := windowsNetworkConflictHint
	t.Cleanup(func() {
		physicalProxyInterface = prevPhysical
		proxyBindingMatches = prevMatches
		validateProxyCore = prevValidate
		startProxyCore = prevStart
		measureProxyDelay = prevDelay
		proxyCoreRunning = prevRunning
		checkProxyRuntimeBinding = prevCheck
		windowsNetworkConflictHint = prevHint
	})

	uplink := "en7"
	var uplinkErr error
	physicalProxyInterface = func(context.Context) (string, error) {
		if uplinkErr != nil {
			return "", uplinkErr
		}
		return uplink, nil
	}
	validateProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	starts := 0
	startProxyCore = func(_ context.Context, profile string, store managedproxy.Store) error {
		starts++
		return os.WriteFile(filepath.Join(managedproxy.Dir(profile), "config.yaml"), []byte("interface-name: "+uplink+"\nmixed-port: "+strconv.Itoa(store.Port)+"\n"), 0600)
	}
	delays := 0
	measureProxyDelay = func(context.Context, string, string) (int, error) {
		delays++
		return 42, nil
	}
	running := false
	proxyCoreRunning = func(context.Context, string, managedproxy.Store) bool { return running }

	delayBody, _ := json.Marshal(proxyInput{NodeID: nodeID})
	// 0. Deliberately stopped core (running=false) must never be resurrected by delay.
	w := request(s, "POST", "/api/proxy/delay", s.origin, string(delayBody), true)
	if w.Code != http.StatusOK || starts != 0 || delays != 1 {
		t.Fatalf("stopped core should not be restarted on delay: code=%d starts=%d delays=%d body=%s", w.Code, starts, delays, w.Body.String())
	}

	// 1. POST /api/proxy/delay while core is running and VM stopped triggers rebind from en0 to en7,
	// rotates ProxyRevision in config.json, and measures delay.
	running = true
	beforeCfg, err := loadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	w = request(s, "POST", "/api/proxy/delay", s.origin, string(delayBody), true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"delay":42`) {
		t.Fatalf("delay after rebind failed: %d %s", w.Code, w.Body.String())
	}
	afterCfg, err := loadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || delays != 2 || afterCfg.ProxyRevision == "" || afterCfg.ProxyRevision == beforeCfg.ProxyRevision {
		t.Fatalf("expected 1 start, 2 delays, and rotated revision; starts=%d delays=%d before=%q after=%q", starts, delays, beforeCfg.ProxyRevision, afterCfg.ProxyRevision)
	}

	// 2. Subsequent call when BindingMatches is true does not restart the core again.
	w = request(s, "POST", "/api/proxy/delay", s.origin, string(delayBody), true)
	if w.Code != http.StatusOK || starts != 1 || delays != 3 {
		t.Fatalf("expected no duplicate restart on matched binding: code=%d starts=%d delays=%d body=%s", w.Code, starts, delays, w.Body.String())
	}

	// 3. When physicalProxyInterface returns error, both delay and probe fail fast before dialing.
	settingsResp := request(s, "GET", "/api/settings", "", "", true)
	if settingsResp.Code != http.StatusOK {
		t.Fatalf("get settings: %d %s", settingsResp.Code, settingsResp.Body.String())
	}
	uplinkErr = errors.New("no physical interface")
	w = request(s, "POST", "/api/proxy/delay", s.origin, string(delayBody), true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "出口网卡已失效") || delays != 3 {
		t.Fatalf("expected fail-fast on delay without dialing: code=%d delays=%d body=%s", w.Code, delays, w.Body.String())
	}
	w = request(s, "POST", "/api/settings/probe", s.origin, settingsResp.Body.String(), true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "出口网卡已失效") {
		t.Fatalf("expected fail-fast on probe: code=%d body=%s", w.Code, w.Body.String())
	}

	// 4. GET /api/proxy/logs reports drift message when drifted and TUN hint when conflict exists.
	uplinkErr = nil
	checkProxyRuntimeBinding = func(context.Context, string) managedproxy.BindingStatus {
		return managedproxy.BindingStatus{Bound: "en0", Current: "en7", Reason: "interface_changed"}
	}
	windowsNetworkConflictHint = func(context.Context, string) string { return "" }
	logsResp := request(s, "GET", "/api/proxy/logs", "", "", true)
	if logsResp.Code != http.StatusOK || !strings.Contains(logsResp.Body.String(), "不再是当前默认物理网卡") {
		t.Fatalf("expected drift message in proxy logs: %d %s", logsResp.Code, logsResp.Body.String())
	}

	checkProxyRuntimeBinding = func(context.Context, string) managedproxy.BindingStatus {
		return managedproxy.BindingStatus{Bound: "en7", Current: "en7"}
	}
	const tunHint = "检测到宿主机存在活跃的 TUN/VPN 默认路由"
	windowsNetworkConflictHint = func(context.Context, string) string { return tunHint }
	logsResp = request(s, "GET", "/api/proxy/logs", "", "", true)
	if logsResp.Code != http.StatusOK || !strings.Contains(logsResp.Body.String(), tunHint) {
		t.Fatalf("expected TUN hint in proxy logs: %d %s", logsResp.Code, logsResp.Body.String())
	}
}
