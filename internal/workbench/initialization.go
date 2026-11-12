package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

// Keep the authorization admitted by the middleware: locking and unlocking
// while an entry request waits must not revive that request in a new epoch.
type initializationEpochKey struct{}

const initializationLockTimeout = 15 * time.Second

var errInitializationCheckBusy = errors.New("后台检查仍在进行，尚未进入工作台，请稍后重试；持续出现时请导出诊断包")

// Only completion may wait for a monitor. Real lifecycle jobs still reject
// immediately, and no detached lock waiter can outlive a canceled request.
func (s *Server) lockInitialization(ctx context.Context, wait bool) error {
	if wait {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, initializationLockTimeout)
		defer cancel()
	}
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errInitializationCheckBusy
			}
			return err
		}
		if s.busy() {
			return errJobBusy
		}
		if s.op.TryLock() {
			return nil
		}
		if !wait {
			return errJobBusy
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Server) writeInitialization(r *http.Request, record initializationRecord) error {
	lifetime := s.lifetimeContext()
	epoch, _ := r.Context().Value(initializationEpochKey{}).(context.Context)
	if s.account == nil {
		return errors.New("Cage 已锁定，请先登录")
	}
	// Linearize this small record write with account revocation. Never hold
	// account.mu during lock acquisition, VM checks or network observation.
	if !s.account.mu.TryLock() {
		// Do not wait behind account I/O after checking the network lease.
		return errors.New("账户状态正在更新，请稍后重新进入工作台")
	}
	defer s.account.mu.Unlock()
	if epoch == nil || epoch != s.account.epoch || epoch.Err() != nil || len(s.account.key) != 32 {
		return errors.New("Cage 登录状态已变化，请重新进入工作台")
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	if err := lifetime.Err(); err != nil {
		return err
	}
	return writePrivate(filepath.Join(s.profile, "initialization.json"), record)
}

type initializationRecord struct {
	Version   int  `json:"version"`
	Completed bool `json:"completed"`
	Started   bool `json:"started,omitempty"`
}

type initializationView struct {
	Required bool `json:"required"`
	Started  bool `json:"started"`
	Tracked  bool `json:"tracked"`
	Prepared bool `json:"prepared"`
}

func (s *Server) initializationState() (initializationView, error) {
	if !s.localVMStorage() || s.storageRecoveryStatus().Required {
		return initializationView{}, nil
	}
	data, err := os.ReadFile(filepath.Join(s.profile, "initialization.json"))
	if err == nil {
		var record initializationRecord
		if json.Unmarshal(data, &record) != nil || record.Version != 1 {
			return initializationView{}, errors.New("初始化记录无法读取，请保留现有数据并导出诊断包")
		}
		return initializationView{Required: !record.Completed || s.macSetupPending(), Started: record.Started, Tracked: true, Prepared: s.environmentPrepared()}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return initializationView{}, err
	}
	// Existing installations predate this screen. A directory or partial disk
	// alone is not proof of a completed Linux installation.
	pending, err := guestos.LoadInstallation(s.profile)
	if err != nil {
		return initializationView{}, err
	}
	if s.macosGuest != nil || s.windowsGuest != nil {
		return initializationView{Required: s.macSetupPending()}, nil
	}
	return initializationView{Required: pending != nil || !s.environmentPrepared(), Started: pending != nil}, nil
}

func (s *Server) finishInitialization(r *http.Request) error {
	if s.macSetupPending() {
		return errors.New("请先完成客机的系统权限确认")
	}
	s.mu.Lock()
	ready, gate := s.ready, s.safety.Gate
	s.mu.Unlock()
	if !ready || !s.vmRunning() {
		return errors.New("客机尚未就绪，请继续安装并完成启动检查")
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	if err := cfg.RequireManagedProxy(s.profile); err != nil {
		return err
	}
	if gate == nil || gate.Mode != cage.GateOpen || !gate.PolicyVerified || gate.Fault || gate.Report == nil || !gate.Until.After(time.Now()) || gate.Configuration != cfg.Fingerprint() || gate.Report.Authorizes(gate.RunID, cfg.Fingerprint(), time.Now()) != nil {
		return errors.New("环境网络检查尚未通过，请在初始化界面重新检查")
	}
	return s.writeInitialization(r, initializationRecord{Version: 1, Completed: true})
}

func (s *Server) initialization(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		state, err := s.initializationState()
		if err != nil {
			requestError(w, err)
			return
		}
		jsonReply(w, state)
		return
	}
	if !s.hasTrustedView(r) || !s.localVMStorage() {
		http.Error(w, "请在本机 Cage 窗口完成初始化", http.StatusForbidden)
		return
	}
	var input struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if err := s.lockInitialization(r.Context(), input.Action == "complete"); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer s.op.Unlock()
	// All state may have changed while waiting. The controller/profile is
	// fixed; a gateway switch or account lock cancels the original request.
	if err := r.Context().Err(); err != nil {
		requestError(w, err)
		return
	}
	if !s.hasTrustedView(r) || !s.localVMStorage() {
		http.Error(w, "请在本机 Cage 窗口完成初始化", http.StatusForbidden)
		return
	}
	if err := s.requireStorageReady(); err != nil {
		requestError(w, err)
		return
	}
	if s.busy() {
		http.Error(w, "环境操作仍在进行，请等待完成后继续", http.StatusConflict)
		return
	}
	state, err := s.initializationState()
	if err != nil {
		requestError(w, err)
		return
	}
	switch input.Action {
	case "begin":
		if state.Required {
			err = s.writeInitialization(r, initializationRecord{Version: 1, Started: state.Started})
		}
	case "install":
		if state.Required {
			err = s.writeInitialization(r, initializationRecord{Version: 1, Started: true})
		}
	case "complete":
		err = s.finishInitialization(r)
	default:
		err = errors.New("未知的初始化操作")
	}
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, map[string]bool{"completed": input.Action == "complete" || !state.Required})
}
