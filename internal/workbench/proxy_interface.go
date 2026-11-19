package workbench

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

var physicalProxyInterface = managedproxy.PhysicalInterface

type proxyInterfaceObservation struct {
	name, configuration, jobID string
	err                        error
}

// Reading the host route can block inside the operating system and does not
// need to serialize with projects, terminals or environment operations.
func (s *Server) observeProxyInterface(ctx context.Context) proxyInterfaceObservation {
	job := s.jobs.snapshot()
	if job.Running {
		return proxyInterfaceObservation{}
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil || cfg.RequireManagedProxy(s.profile) != nil {
		return proxyInterfaceObservation{}
	}
	name, err := physicalProxyInterface(ctx)
	return proxyInterfaceObservation{name: name, configuration: cfg.Fingerprint(), jobID: job.ID, err: err}
}

// The lifecycle lock covers effects such as revocation and recovery admission.
// Discard observations made before a configuration change or a lifecycle job.
func (s *Server) applyProxyInterfaceObservation(observed proxyInterfaceObservation) {
	job := s.jobs.snapshot()
	if observed.configuration == "" || job.Running || job.ID != observed.jobID {
		return
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil || cfg.Fingerprint() != observed.configuration || cfg.RequireManagedProxy(s.profile) != nil {
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
	bound, err := managedproxy.BoundInterface(filepath.Join(managedproxy.Dir(s.profile), "config.yaml"))
	if err != nil || (bound == observed.name && !lost) {
		return
	}
	if lost && time.Now().Before(retry) {
		return
	}
	if !s.vmRunning() {
		return
	}
	// Revoke the previous report before the asynchronous recovery can start.
	if err := s.closeProxyGate("物理网卡已变化，正在重新绑定代理并检查出口"); err != nil {
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
