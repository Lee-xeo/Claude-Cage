package workbench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sylvinus/agent-vm/internal/containerenv"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

var physicalProxyInterface = managedproxy.PhysicalInterface
var proxyBindingMatches = managedproxy.BindingMatches
var proxyInterfaceAvailable = managedproxy.InterfaceAvailable
var proxyInterfaceCoreRunning = managedproxy.Running

type proxyInterfaceObservation struct {
	name, configuration, jobID string
	bound                      string
	bindingMatches             bool
	bindingErr                 error
	err                        error
	stoppedCoreDeadInterface   bool
}

// Reading the host route can block inside the operating system and does not
// need to serialize with projects, terminals or environment operations.
func (s *Server) observeProxyInterface(ctx context.Context) proxyInterfaceObservation {
	job := s.jobs.snapshot()
	if job.Running {
		return proxyInterfaceObservation{}
	}
	vmRunning := s.vmRunning()
	// While a local VM is stopped no guest traffic uses the proxy, and the
	// start path checks and rebinds the interface itself; skip reading the host
	// route (three processes per tick on macOS) as long as the bound interface
	// still exists and holds an IPv4 address. Only when the bound interface has
	// vanished/dropped while the proxy core is running do we probe the new
	// uplink so the core self-heals instead of staying offline (#781).
	if !containerenv.Configured(s.profile) && !vmRunning {
		cfg, err := loadConfig(s.configPath)
		if err != nil {
			return proxyInterfaceObservation{}
		}
		configPath := filepath.Join(managedproxy.Dir(s.profile), "config.yaml")
		bound, bindingErr := managedproxy.BoundInterface(configPath)
		if bindingErr != nil || bound == "" {
			return proxyInterfaceObservation{}
		}
		if s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) != nil {
			return proxyInterfaceObservation{}
		}
		if proxyInterfaceAvailable(bound) {
			return proxyInterfaceObservation{}
		}
		p, err := s.proxyStore.Load(s.profile)
		if err != nil {
			return proxyInterfaceObservation{}
		}
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		running := proxyInterfaceCoreRunning(checkCtx, s.profile, p)
		cancel()
		if !running {
			return proxyInterfaceObservation{}
		}
		observed := proxyInterfaceObservation{
			configuration:            cfg.Fingerprint(),
			jobID:                    job.ID,
			bound:                    bound,
			stoppedCoreDeadInterface: true,
		}
		observed.name, observed.err = physicalProxyInterface(ctx)
		if observed.err == nil && observed.bound == observed.name {
			observed.bindingMatches = proxyBindingMatches(configPath, observed.name)
		}
		return observed
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
	if !vmRunning && observed.bindingErr == nil && observed.bound != "" && !proxyInterfaceAvailable(observed.bound) {
		if p, loadErr := s.proxyStore.Load(s.profile); loadErr == nil {
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			running := proxyInterfaceCoreRunning(checkCtx, s.profile, p)
			cancel()
			observed.stoppedCoreDeadInterface = running
		}
	}
	observed.name, observed.err = physicalProxyInterface(ctx)
	if observed.err == nil && observed.bindingErr == nil && observed.bound == observed.name {
		observed.bindingMatches = proxyBindingMatches(configPath, observed.name)
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
		if !observed.stoppedCoreDeadInterface {
			return
		}
		s.mu.Lock()
		s.proxyNetworkLost = true
		s.proxyNetworkRetry = time.Now().Add(15 * time.Second)
		s.mu.Unlock()
		diagnostics.Log("warn", "proxy", "代理核心绑定的出口网卡已失效，正在自动重新绑定")
		_, _ = s.submitJob("proxy-network", s.rebindProxyInterface)
		return
	}
	// Revoke the previous report before the asynchronous recovery can start.
	reason := "物理网卡已变化，正在重新绑定代理并检查出口"
	if observed.bound == observed.name && !lost {
		reason = "代理节点路由接口已变化，正在重新绑定代理并检查出口"
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

func (s *Server) checkProxyInterfaceBinding(ctx context.Context) error {
	configPath := filepath.Join(managedproxy.Dir(s.profile), "config.yaml")
	bound, err := managedproxy.BoundInterface(configPath)
	if err != nil {
		return err
	}
	current, err := physicalProxyInterface(ctx)
	if err != nil {
		return err
	}
	if !proxyBindingMatches(configPath, current) {
		return &managedproxy.StaleInterfaceError{Bound: bound, Current: current}
	}
	return nil
}

// ensureProxyInterfaceBeforeUse verifies that an active, running proxy core is
// still bound to the host's current physical uplink before an explicit probe or
// latency test. When no VM is running and no lifecycle job holds s.op, a stale
// binding is automatically rebound so the core recovers without manual restart.
func (s *Server) ensureProxyInterfaceBeforeUse(ctx context.Context) error {
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return nil
	}
	if s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) != nil {
		return nil
	}
	configPath := filepath.Join(managedproxy.Dir(s.profile), "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		return nil
	}
	p, err := s.proxyStore.Load(s.profile)
	if err != nil {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	running := proxyInterfaceCoreRunning(checkCtx, s.profile, p)
	cancel()
	if !running {
		return nil
	}
	err = s.checkProxyInterfaceBinding(ctx)
	if err == nil {
		return nil
	}
	var stale *managedproxy.StaleInterfaceError
	if !errors.As(err, &stale) {
		return err
	}
	if s.vmRunning() || !s.op.TryLock() {
		return err
	}
	defer s.op.Unlock()
	if s.vmRunning() {
		return err
	}
	if recheckErr := s.checkProxyInterfaceBinding(ctx); recheckErr == nil {
		return nil
	}
	cfg, cfgErr := loadConfig(s.configPath)
	if cfgErr != nil {
		return err
	}
	if s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) != nil {
		return err
	}
	p, loadErr := s.proxyStore.Load(s.profile)
	if loadErr != nil {
		return err
	}
	diagnostics.Log("warn", "proxy", "检测到代理核心出口网卡已变化，正在自动重新绑定")
	if startErr := startProxyCore(ctx, s.profile, p); startErr != nil {
		return startErr
	}
	s.mu.Lock()
	s.proxyNetworkLost = false
	s.proxyNetworkRetry = time.Time{}
	s.safety.monitorError = ""
	s.mu.Unlock()
	return nil
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
