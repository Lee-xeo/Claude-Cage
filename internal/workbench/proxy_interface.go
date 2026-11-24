package workbench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sylvinus/agent-vm/internal/containerenv"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

var (
	physicalProxyInterface     = managedproxy.PhysicalInterface
	proxyBindingMatches        = managedproxy.BindingMatches
	proxyBindingAddressChanged = managedproxy.BindingAddressChanged
	checkProxyRuntimeBinding   = managedproxy.CheckRuntimeBinding
	windowsNetworkConflictHint = managedproxy.WindowsNetworkConflictHint
)

type proxyInterfaceObservation struct {
	name, configuration, jobID string
	bound                      string
	bindingMatches             bool
	addressChanged             bool
	bindingErr                 error
	err                        error
}

// Reading the host route can block inside the operating system and does not
// need to serialize with projects, terminals or environment operations.
func (s *Server) observeProxyInterface(ctx context.Context) proxyInterfaceObservation {
	job := s.jobs.snapshot()
	if job.Running {
		return proxyInterfaceObservation{}
	}
	// While a local VM is stopped no guest traffic uses the proxy, and the
	// start path checks and rebinds the interface itself; skip reading the host
	// route (three processes per tick on macOS). A container's state query
	// would itself run engine commands, so containers keep observing.
	if !containerenv.Configured(s.profile) && !s.vmRunning() {
		return proxyInterfaceObservation{}
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return proxyInterfaceObservation{}
	}
	observed := proxyInterfaceObservation{configuration: cfg.Fingerprint(), jobID: job.ID}
	configPath := filepath.Join(managedproxy.Dir(s.profile), "config.yaml")
	observed.bound, observed.bindingErr = managedproxy.BoundInterface(configPath)
	if errors.Is(observed.bindingErr, managedproxy.ErrLocalProxyNode) {
		return observed
	}
	if s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) != nil {
		return proxyInterfaceObservation{}
	}
	observed.name, observed.err = physicalProxyInterface(ctx)
	if observed.err == nil && observed.bindingErr == nil && observed.bound == observed.name {
		observed.bindingMatches = proxyBindingMatches(configPath, observed.name)
		if !observed.bindingMatches {
			observed.addressChanged = proxyBindingAddressChanged(configPath, observed.name)
		}
	}
	return observed
}

// The lifecycle lock covers effects such as revocation and recovery admission.
// Discard observations made before a configuration change or a lifecycle job.
func (s *Server) applyProxyInterfaceObservation(observed proxyInterfaceObservation) {
	job := s.jobs.snapshot()
	if observed.configuration == "" || job.Running || job.ID != observed.jobID {
		return
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil || cfg.Fingerprint() != observed.configuration {
		return
	}
	if errors.Is(observed.bindingErr, managedproxy.ErrLocalProxyNode) {
		if err := s.retireLocalProxyCore(); err != nil {
			diagnostics.Log("error", "proxy", "停止旧代理核心失败："+err.Error())
		}
		return
	}
	if s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) != nil {
		return
	}
	if observed.err != nil {
		reason := observed.err.Error()
		_ = s.closeProxyGate(reason)
		s.mu.Lock()
		s.safety.monitorError = reason
		s.proxyNetworkLost = true
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	lost := s.proxyNetworkLost
	retry := s.proxyNetworkRetry
	s.mu.Unlock()
	if observed.bindingErr != nil && !(lost && os.IsNotExist(observed.bindingErr)) || (observed.bindingMatches && !lost) {
		return
	}
	if lost && time.Now().Before(retry) {
		return
	}
	if !s.vmRunning() {
		return
	}
	// Revoke the previous report before the asynchronous recovery can start.
	reason := "物理网卡已变化，正在重新绑定代理并检查出口"
	if observed.bound == observed.name && !lost {
		if observed.addressChanged {
			reason = "物理网卡地址或状态已变化，正在重新绑定代理并检查出口"
		} else {
			reason = "代理节点路由接口已变化，正在重新绑定代理并检查出口"
		}
	}
	if err := s.closeProxyGate(reason); err != nil {
		return
	}
	s.mu.Lock()
	s.proxyNetworkLost = true
	s.proxyNetworkRetry = time.Now().Add(15 * time.Second)
	s.mu.Unlock()
	_, _ = s.submitJob("proxy-network", s.rebindProxyInterface)
}

func (s *Server) rebindProxyInterface() (returned error) {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if returned == nil {
			s.proxyNetworkLost = false
			s.proxyNetworkRetry = time.Time{}
			s.safety.monitorError = ""
			return
		}
		// Keep the outage pending even if the restored uplink has the same
		// interface name. Only a complete rebind/check resolves the outage.
		s.proxyNetworkLost = true
		s.proxyNetworkRetry = time.Now().Add(15 * time.Second)
		if !errors.Is(returned, context.Canceled) {
			s.safety.monitorError = diagnostics.Redact(usererror.Display("恢复代理连接", returned, "连接恢复后会自动重试，也可手动重新检测。").Error())
		}
	}()
	s.setMessage("正在识别物理网卡并重新绑定代理…")
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	p, err := managedproxy.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision)
	if err != nil {
		return err
	}
	return s.applyProxy(p, cfg, nil, false)
}

func (s *Server) ensureStoppedProxyBinding(ctx context.Context) error {
	if s.vmRunning() || s.jobs.snapshot().Running {
		return nil
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil || s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) != nil {
		return nil
	}
	configPath := filepath.Join(managedproxy.Dir(s.profile), "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		return nil
	}
	isCoreRunning := func(p managedproxy.Store) bool {
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return proxyCoreRunning(runCtx, s.profile, p)
	}
	p, err := s.proxyStore.Load(s.profile)
	if err != nil || !isCoreRunning(p) {
		return nil
	}
	name, err := physicalProxyInterface(ctx)
	if err != nil {
		return fmt.Errorf("代理核心绑定的出口网卡已失效：%w", err)
	}
	if proxyBindingMatches(configPath, name) {
		return nil
	}

	s.op.Lock()
	defer s.op.Unlock()
	if s.vmRunning() || s.jobs.snapshot().Running {
		return nil
	}
	cfg, err = loadConfig(s.configPath)
	if err != nil {
		return nil
	}
	p, err = managedproxy.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision)
	if err != nil || !isCoreRunning(p) {
		return nil
	}
	if _, err := os.Stat(configPath); err != nil {
		return nil
	}
	name, err = physicalProxyInterface(ctx)
	if err != nil {
		return fmt.Errorf("代理核心绑定的出口网卡已失效：%w", err)
	}
	if proxyBindingMatches(configPath, name) {
		return nil
	}

	previous, oldCfg := p, cfg
	p.GuestDNS, p.BootstrapDNS = cfg.DNSEndpoints(), cfg.BootstrapEndpoints()
	if err := validateProxyCore(ctx, s.profile, p); err != nil {
		return err
	}
	if err := managedproxy.Stop(s.profile); err != nil {
		return err
	}
	p.Revision = managedproxy.Token()
	cfg.ProxyRevision = p.Revision
	if err := p.Save(s.profile); err != nil {
		_ = previous.Save(s.profile)
		return err
	}
	if err := writePrivate(s.configPath, cfg); err != nil {
		_ = previous.Save(s.profile)
		_ = writePrivate(s.configPath, oldCfg)
		return err
	}
	if err := startProxyCore(ctx, s.profile, p); err != nil {
		_ = previous.Save(s.profile)
		_ = writePrivate(s.configPath, oldCfg)
		return err
	}
	diagnostics.Log("info", "proxy", "rebound managed proxy core to current physical interface while environment was stopped")
	return nil
}
