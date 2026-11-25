package workbench

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/containerenv"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
)

func (s *Server) localVMStorage() bool {
	return !s.clientOnly && !containerenv.Configured(s.profile)
}

type storageRecoveryView struct {
	Required      bool   `json:"required"`
	Error         string `json:"error,omitempty"`
	RepairAllowed bool   `json:"repair_allowed"`
}

func (s *Server) storageRecoveryStatusLocked() storageRecoveryView {
	return storageRecoveryView{
		Required: s.storageRecoveryError != "", Error: s.storageRecoveryError,
	}
}

func (s *Server) storageRecoveryStatus() storageRecoveryView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storageRecoveryStatusLocked()
}

func (s *Server) requireStorageReady() error {
	state := s.storageRecoveryStatus()
	if state.Required {
		return fmt.Errorf("环境存储待恢复，请在设置中恢复后继续。%s", state.Error)
	}
	return nil
}

func (s *Server) failStorageRecovery(err error) error {
	s.mu.Lock()
	s.storageRecoveryError = err.Error()
	s.ready = false
	s.mu.Unlock()
	diagnostics.Log("error", "storage", "environment storage recovery failed: "+err.Error())
	return err
}

// Recovery keeps local account access and host diagnostics available. Guest
// reads can also create sessions, so admission names the host-only reads.
func storageRecoveryRoute(r *http.Request) bool {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/api/account/") || strings.HasPrefix(path, "/api/diagnostics") {
		return true
	}
	switch path {
	case "/api/account", "/api/session", "/api/launch", "/api/handoff", "/api/remote/manifest", "/api/environment/storage/recover", "/api/environment/device-reset/preview", "/api/environment/device-reset/execute":
		return true
	}
	if r.Method == http.MethodGet {
		switch path {
		case "/api/status", "/api/settings", "/api/appearance", "/api/favorites", "/api/proxy", "/api/agent/routes", "/api/environment/storage", "/api/environment/initialization", "/api/environment/device-reset":
			return true
		}
	}
	return false
}

func (s *Server) recoverEnvironmentStorage(w http.ResponseWriter, r *http.Request) {
	if !s.localVMStorage() {
		http.Error(w, "请在虚拟机所在电脑上恢复环境存储", http.StatusConflict)
		return
	}
	if !s.storageRecoveryStatus().Required {
		http.Error(w, "当前环境存储无需恢复", http.StatusConflict)
		return
	}
	var input struct {
		Repair bool `json:"repair"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	s.launchRequest(w, r, "storage-recovery", func() error {
		s.setMessage("正在恢复环境存储并检查数据位置…")
		if err := cage.RecoverStorage(s.operationContext(), s.profile, nil); err != nil {
			return s.failStorageRecovery(err)
		}
		if _, err := cage.GetStorageLocation(s.profile); err != nil {
			return s.failStorageRecovery(err)
		}
		if err := s.prepareEnvironment(); err != nil {
			return s.failStorageRecovery(err)
		}
		s.mu.Lock()
		s.storageRecoveryError = ""
		s.mu.Unlock()
		s.setMessage("环境存储已恢复，可以启动环境")
		return nil
	})
}

func (s *Server) environmentStorage(w http.ResponseWriter, r *http.Request) {
	if !s.localVMStorage() {
		http.Error(w, "仅本机虚拟机支持调整存储位置", http.StatusConflict)
		return
	}
	if r.Method == http.MethodGet {
		if recovery := s.storageRecoveryStatus(); recovery.Required {
			jsonReply(w, map[string]any{"recovery": recovery})
			return
		}
		location, err := cage.GetStorageLocation(s.profile)
		if errors.Is(err, cage.ErrStorageMissing) {
			jsonReply(w, struct {
				cage.StorageLocation
				Error string `json:"error"`
			}{location, err.Error()})
			return
		}
		if err != nil {
			if errors.Is(err, cage.ErrStorageRuntimeRecovery) {
				s.failStorageRecovery(err)
				jsonReply(w, map[string]any{"recovery": s.storageRecoveryStatus()})
				return
			}
			requestError(w, err)
			return
		}
		suggested := ""
		if !location.Custom && !location.HasEnvironment {
			initialization, err := s.initializationState()
			if err != nil {
				requestError(w, err)
				return
			}
			if initialization.Required && !initialization.Started && !initialization.Prepared {
				suggested = cage.InitialStorageDirectory(s.exe)
			}
		}
		jsonReply(w, struct {
			cage.StorageLocation
			SuggestedDirectory string `json:"suggested_directory,omitempty"`
		}{location, suggested})
		return
	}

	var input struct {
		Directory string `json:"directory"`
		Previous  string `json:"previous"`
		Migrate   bool   `json:"migrate"`
		Missing   bool   `json:"missing"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	s.launchRequest(w, r, "storage", func() error {
		if err := cage.RecoverStorage(s.operationContext(), s.profile, nil); err != nil {
			return s.failStorageRecovery(err)
		}
		current, err := cage.GetStorageLocation(s.profile)
		if err != nil && !(input.Missing && errors.Is(err, cage.ErrStorageMissing)) {
			return err
		}
		if current.Directory != input.Previous || current.HasEnvironment != input.Migrate || current.Missing != input.Missing {
			return errors.New("环境或存储位置已改变，请重新打开设置后再选择")
		}
		if err = cage.ValidateStorageTarget(s.profile, input.Directory); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return errors.New("无法读取或写入所选存储目录，请在当前页面选择有权限的本地文件夹后重试，已有数据已保留")
			}
			return err
		}
		if err = s.validateStoragePlacement(input.Directory); err != nil {
			return err
		}
		if current.HasEnvironment {
			if err = s.stopEnvironment(); err != nil {
				return err
			}
		}
		s.setMessage("正在检查环境存储…")
		var runtime cage.StorageRuntime
		_, err = cage.MoveStorageWithRuntime(s.operationContext(), s.profile, input.Directory, current, runtime, func(copied, total int64) {
			s.setMessage(fmt.Sprintf("正在迁移环境数据 · %.1f / %.1f GiB", float64(copied)/(1<<30), float64(total)/(1<<30)))
		})
		if err != nil {
			if _, recoveryErr := cage.GetStorageLocation(s.profile); errors.Is(recoveryErr, cage.ErrStorageRuntimeRecovery) {
				return s.failStorageRecovery(err)
			}
		}
		if errors.Is(err, os.ErrPermission) {
			return errors.New("无法写入所选存储目录。请在当前页面选择有写入权限的本地文件夹后重试，已有数据已保留")
		}
		return err
	})
}
