package workbench

import "github.com/sylvinus/agent-vm/internal/containerenv"

// Lima can change its base share only while stopped. Reconnecting a running
// instance must not keep serving the former project as the transfer directory.
func (s *Server) requireCurrentLinuxTransferMount() error {
	if containerenv.Configured(s.profile) || !s.vmRunning() {
		return nil
	}
	if shares, _ := s.inspectVMLayout(); shares {
		return nil
	}
	failure := &projectShareError{Recovery: "environment-restart", Message: "下载目录与运行中的文件共享不一致。请先保存工作，停止环境后重新启动；项目文件、会话和登录状态会保留。"}
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	_ = s.closeProxyGate(failure.Message)
	return failure
}
