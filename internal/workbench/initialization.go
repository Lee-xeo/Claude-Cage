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

func (s *Server) writeInitialization(r *http.Request, record initializationRecord) error {
	lifetime := s.lifetimeContext()
	epoch, _ := r.Context().Value(accountEpochKey{}).(context.Context)
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

const desktopOnboardingLease = 15 * time.Second

// touchDesktopOnboarding renews the lease from the onboarding screen's polls.
// After an agent releases it, polling alone does not reclaim it; only a
// desktop setup action (claimDesktopOnboarding) does.
func (s *Server) touchDesktopOnboarding() {
	s.mu.Lock()
	if !s.desktopOnboardingReleased {
		s.desktopOnboardingUntil = time.Now().Add(desktopOnboardingLease)
	}
	s.mu.Unlock()
}

func (s *Server) claimDesktopOnboarding() {
	s.mu.Lock()
	s.desktopOnboardingReleased = false
	s.desktopOnboardingUntil = time.Now().Add(desktopOnboardingLease)
	s.mu.Unlock()
}

// releaseDesktopOnboarding hands first-run setup to the agent without
// quitting the window, which would also lock the account.
func (s *Server) releaseDesktopOnboarding() {
	s.mu.Lock()
	s.desktopOnboardingReleased = true
	s.desktopOnboardingUntil = time.Time{}
	s.mu.Unlock()
}

func (s *Server) clearDesktopOnboarding() {
	s.mu.Lock()
	s.desktopOnboardingReleased = false
	s.desktopOnboardingUntil = time.Time{}
	s.mu.Unlock()
}

func (s *Server) desktopOnboardingLeaseActive() bool {
	s.mu.Lock()
	active := !s.desktopOnboardingUntil.IsZero() && time.Now().Before(s.desktopOnboardingUntil)
	s.mu.Unlock()
	return active
}

func (s *Server) desktopOnboardingActive() bool {
	if !s.desktopOnboardingLeaseActive() {
		return false
	}
	state, err := s.initializationState()
	return err == nil && state.Required
}

type initializationRecord struct {
	Version   int  `json:"version"`
	Completed bool `json:"completed"`
	Started   bool `json:"started,omitempty"`
}

type initializationView struct {
	Required      bool `json:"required"`
	Started       bool `json:"started"`
	Tracked       bool `json:"tracked"`
	Prepared      bool `json:"prepared"`
	DesktopActive bool `json:"desktop_active,omitempty"`
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
		if _, err := guestos.LoadInstallation(s.profile); err != nil {
			return initializationView{}, err
		}
		required := !record.Completed
		return initializationView{Required: required, Started: record.Started, Tracked: true, Prepared: s.environmentPrepared(), DesktopActive: required && s.desktopOnboardingLeaseActive()}, nil
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
	prepared := s.environmentPrepared()
	required := pending != nil || !prepared
	return initializationView{Required: required, Started: pending != nil, Prepared: prepared, DesktopActive: required && s.desktopOnboardingLeaseActive()}, nil
}

func (s *Server) finishInitialization(r *http.Request) error {
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
		if requestInitiator(r) == jobInitiatorSetup {
			s.touchDesktopOnboarding()
		}
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
	if input.Action == "complete" {
		if err := s.lockCompletion(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	} else if !s.op.TryLock() {
		http.Error(w, errJobBusy.Error(), http.StatusConflict)
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
			if err == nil && requestInitiator(r) != jobInitiatorAgent {
				s.claimDesktopOnboarding()
			}
		}
	case "install":
		if state.Required {
			err = s.writeInitialization(r, initializationRecord{Version: 1, Started: true})
			if err == nil && requestInitiator(r) != jobInitiatorAgent {
				s.claimDesktopOnboarding()
			}
		}
	case "release":
		if state.Required {
			err = s.writeInitialization(r, initializationRecord{Version: 1, Started: state.Started})
			if err == nil {
				s.releaseDesktopOnboarding()
			}
		}
	case "complete":
		err = s.finishInitialization(r)
		if err == nil {
			s.clearDesktopOnboarding()
		}
	default:
		err = errors.New("未知的初始化操作")
	}
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, map[string]bool{"completed": input.Action == "complete" || !state.Required})
}
