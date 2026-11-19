package workbench

import (
	"net/http"
)

func (s *Server) accountStatus(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, s.accountView(r))
}

func (s *Server) accountView(r *http.Request) accountView {
	v := s.account.viewFor(s.hasTrustedView(r))
	v.BackgroundRunning = s.vmRunning()
	return v
}

func (s *Server) accountAction(w http.ResponseWriter, r *http.Request) {
	if s.account == nil {
		http.Error(w, "本地账户尚未初始化", 503)
		return
	}
	action := r.PathValue("action")
	if action != "create" && action != "unlock" && action != "profile" && action != "password" && action != "lock" && action != "logout" && action != "suspend" && action != "device-enroll" && action != "device-revoke" && action != "device-unlock" && action != "recovery-generate" && action != "recovery-reset" {
		http.NotFound(w, r)
		return
	}
	if action != "create" && action != "unlock" && action != "device-unlock" && action != "recovery-reset" {
		if _, ok := s.account.accessFor(s.hasTrustedView(r), false); !ok {
			http.Error(w, "请先解锁 Cage", http.StatusLocked)
			return
		}
	}
	if (action == "lock" || action == "logout" || action == "device-enroll") && s.account.view().TrustedLocal {
		http.Error(w, "请先在本机的「设置 → 账户」设置密码", http.StatusConflict)
		return
	}
	var input struct {
		Username        string           `json:"username"`
		Password        string           `json:"password"`
		ConfirmPassword string           `json:"confirm_password"`
		OldPassword     string           `json:"old_password"`
		RecoveryKey     string           `json:"recovery_key"`
		Avatar          string           `json:"avatar"`
		Kind            string           `json:"kind"`
		Credential      deviceCredential `json:"credential"`
	}
	if !readJSON(w, r, &input, 3<<20) {
		return
	}
	s.account.mu.Lock()
	vaultLease := s.account.vaultLease
	s.account.mu.Unlock()
	var err error
	var recoveryKey string
	if action == "password" || action == "recovery-reset" {
		if !s.op.TryLock() {
			http.Error(w, "环境操作仍在进行，请等待完成后设置密码", http.StatusConflict)
			return
		}
		defer s.op.Unlock()
		if s.busy() {
			http.Error(w, "环境操作仍在进行，请等待完成后设置密码", http.StatusConflict)
			return
		}
	}
	switch action {
	case "create":
		if input.Password != input.ConfirmPassword {
			http.Error(w, "两次密码不一致", 400)
			return
		}
		recoveryKey, err = s.account.createAccount(input.Username, input.Password, input.Avatar)
	case "recovery-generate":
		recoveryKey, err = s.account.generateRecovery(input.Password)
	case "recovery-reset":
		if input.Password != input.ConfirmPassword {
			http.Error(w, "两次密码不一致", http.StatusBadRequest)
			return
		}
		recoveryKey, err = s.account.resetPassword(input.RecoveryKey, input.Password)
	case "unlock":
		err = s.account.unlock(input.Password)
	case "profile":
		err = s.account.updateProfile(input.Username, input.Avatar)
	case "password":
		if input.Password != input.ConfirmPassword {
			http.Error(w, "两次密码不一致", 400)
			return
		}
		err = s.account.changePassword(input.OldPassword, input.Password)
	case "lock":
		err = s.account.lockAccess(false)
	case "suspend":
		s.account.lock()
	case "device-enroll":
		var credential deviceCredential
		credential, err = s.account.enrollDevice(input.Kind)
		if err == nil {
			jsonReply(w, credential)
			return
		}
	case "device-revoke":
		err = s.account.revokeDevice(input.Kind)
	case "device-unlock":
		err = s.account.unlockDevice(input.Kind, input.Credential)
	case "logout":
		s.requestStop(w, true)
		return
	}
	if err != nil {
		requestError(w, err)
		return
	}
	if action == "unlock" || action == "device-unlock" || action == "recovery-reset" {
		s.refreshShellLoginWatch()
	}
	if action == "lock" || action == "suspend" || action == "recovery-reset" {
		s.clearBrowserVault(vaultLease)
	}
	if recoveryKey != "" {
		jsonReply(w, struct {
			accountView
			RecoveryKey string `json:"recovery_key"`
		}{s.accountView(r), recoveryKey})
		return
	}
	s.accountStatus(w, r)
}
