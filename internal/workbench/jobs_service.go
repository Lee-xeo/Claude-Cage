package workbench

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *Server) busy() bool { return s.jobs.snapshot().Running }

// Completion can race the short background status probes even when no job is
// running. Wait briefly for those probes instead of rejecting a user's click.
// Callers still recheck readiness and network evidence under the acquired lock.
func (s *Server) lockCompletion(parent context.Context) error {
	ctx, cancel := context.WithTimeoutCause(parent, 15*time.Second, errors.New("后台检查仍在进行，尚未进入工作台，请稍后重试；持续出现时请导出诊断包"))
	defer cancel()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		if s.busy() {
			return errJobBusy
		}
		if s.op.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	return context.Cause(ctx)
}

// submitJob keeps network intent and account policy in the workbench. The
// manager owns admission, execution, cancellation and published job state.
func (s *Server) submitJob(kind string, run func() error) (Job, error) {
	return s.submitJobWithInitiator(jobInitiatorDesktop, kind, run)
}

func (s *Server) submitJobWithInitiator(initiator, kind string, run func() error) (Job, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return Job{}, errJobsClosed
	}
	if s.storageRecoveryError != "" && kind != "storage-recovery" && kind != "device-reset" {
		err := fmt.Errorf("环境存储待恢复，请在设置中恢复后继续。%s", s.storageRecoveryError)
		s.mu.Unlock()
		return Job{}, err
	}
	var afterSuccess func() error
	if kind == "logout" {
		kind = "stop"
		afterSuccess = func() error { return s.account.lockAccess(true) }
	}
	intentFailed := false
	job, err := s.jobs.submitWithInitiator(&s.op, kind, initiator, s.safety.pauseVersion, func() error {
		if kind == "safety" || (kind == "start" || kind == "restore") {
			if err := s.saveNetworkIntentLocked(false); err != nil {
				intentFailed = true
				return err
			}
		}
		if s.monitorCancel != nil {
			s.monitorCancel()
		}
		return nil
	}, afterSuccess, run)
	s.mu.Unlock()
	if intentFailed {
		_ = s.closeProxyGate("网络恢复意图无法保存，外网保持关闭")
	}
	return job, err
}

func (s *Server) setMessage(message string) { s.jobs.execution().message(message) }

func (s *Server) operationContext() context.Context {
	execution := s.jobs.execution()
	if execution.id != "" {
		return execution.ctx
	}
	return s.lifetimeContext()
}
