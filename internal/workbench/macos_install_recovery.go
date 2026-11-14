package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

func macOSInstallRecoveryRoute(method, path string) bool {
	if path == "/api/diagnostics" || path == "/api/diagnostics/export" ||
		strings.HasPrefix(path, "/api/diagnostics/events") {
		return true
	}
	if method == http.MethodGet {
		switch path {
		case "/api/account", "/api/status", "/api/proxy", "/api/proxy/connections", "/api/proxy/logs",
			"/api/environment/initialization", "/api/environment/guest-systems", "/api/host/git",
			"/api/macos/install-recovery", "/api/remote/manifest":
			return true
		}
		return strings.HasPrefix(path, "/api/agent/jobs/")
	}
	if method == http.MethodPost {
		switch path {
		case "/api/account/unlock", "/api/account/lock", "/api/environment/initialization",
			"/api/environment/guest-systems/activate", "/api/host/git", "/api/stop", "/api/start",
			"/api/macos/install-recovery", "/api/session", "/api/launch", "/api/handoff":
			return true
		}
	}
	// Re-selection of the guest system is the one destructive call the recovery
	// mode needs: an unstarted installation has no disk to protect yet, and the
	// DELETE handler still refuses once any partial VM exists.
	if method == http.MethodDelete {
		return path == "/api/environment/guest-systems"
	}
	return false
}

// recordInstallRecovery keeps the precise, sanitized cause of a refused
// recovery operation in the local diagnostics log. Client messages stay
// generic on purpose: they never reveal the guest account or machine
// identity that the recovery record protects.
func (s *Server) recordInstallRecovery(detail string) {
	_ = diagnostics.New(s.profile).Record("warn", "macos-install-recovery", detail)
}

// macOSInstallRecovery exposes only the pending VM's setup screen and fixed
// lifecycle operations. It never creates or substitutes a MacOSProfile.
func (s *Server) macOSInstallRecovery(w http.ResponseWriter, r *http.Request) {
	if !s.hasTrustedView(r) || !s.localVMStorage() {
		http.Error(w, "请在本机 Cage 窗口继续安装", http.StatusForbidden)
		return
	}
	recovery, err := guestos.LoadMacOSInstallRecovery(s.profile)
	if err != nil {
		s.recordInstallRecovery("恢复入口身份校验未通过：" + err.Error())
		http.Error(w, "安装恢复入口身份校验未通过，原磁盘和安装记录已保留；请稍后重试，仍失败时可在“日志与诊断”导出诊断包排查", http.StatusConflict)
		return
	}
	if s.macosGuest != nil {
		s.recordInstallRecovery("已有绑定的 macOS 客机，安装恢复入口停用")
		http.Error(w, "已有绑定的 macOS 客机，无需恢复安装；原磁盘和安装记录已保留", http.StatusConflict)
		return
	}
	if recovery == nil {
		if r.Method == http.MethodGet {
			install, loadErr := guestos.LoadInstallation(s.profile)
			canPrepare := false
			if loadErr == nil && install != nil && install.System == guestos.MacOS {
				canPrepare, loadErr = guestos.CanRecoverMacOSInstall(s.profile, install.ID)
			}
			if loadErr != nil {
				s.recordInstallRecovery("确认原安装记录可恢复性失败：" + loadErr.Error())
				http.Error(w, "暂时无法确认原安装记录是否可恢复，请稍后重试；现有数据已保留，可在“日志与诊断”导出诊断包排查", http.StatusConflict)
				return
			}
			jsonReply(w, map[string]bool{"available": false, "can_prepare": canPrepare})
			return
		}
		var input guestos.MacLoginRequest
		if !readJSON(w, r, &input) || input.Action != "prepare" {
			http.Error(w, "请先确认原安装记录", http.StatusConflict)
			return
		}
		install, loadErr := guestos.LoadInstallation(s.profile)
		if loadErr != nil {
			s.recordInstallRecovery("读取原安装记录失败：" + loadErr.Error())
			http.Error(w, "暂时无法读取原安装记录，请稍后重试；现有数据已保留，可在“日志与诊断”导出诊断包排查", http.StatusConflict)
			return
		}
		if install == nil || install.System != guestos.MacOS {
			s.recordInstallRecovery("准备恢复入口被拒绝：没有对应的 macOS 安装记录")
			http.Error(w, "没有可恢复的 macOS 安装记录，请回到初始化页重新确认", http.StatusConflict)
			return
		}
		credential, loadErr := s.account.macGuestCredentials(r.Context(), install.ID)
		if loadErr != nil {
			s.recordInstallRecovery("读取原客机凭据失败：" + loadErr.Error())
			http.Error(w, "暂时无法读取原客机凭据，请确认 Cage 账密后重试；原账户和磁盘已保留", http.StatusConflict)
			return
		}
		if !guestos.ValidMacOSAccountName(credential.Username) || credential.Password == "" {
			s.recordInstallRecovery("准备恢复入口被拒绝：原客机凭据不完整")
			http.Error(w, "原客机凭据不完整，请在设置中确认 Cage 账密后重试；原账户和磁盘已保留", http.StatusConflict)
			return
		}
		canRecover, loadErr := guestos.CanRecoverMacOSInstall(s.profile, install.ID)
		if loadErr != nil {
			s.recordInstallRecovery("核对原客机磁盘身份失败：" + loadErr.Error())
			http.Error(w, "暂时无法核对原客机磁盘身份，请稍后重试；磁盘和安装记录已保留", http.StatusConflict)
			return
		}
		if !canRecover {
			s.recordInstallRecovery("准备恢复入口被拒绝：原客机磁盘或机器标识尚未就绪")
			http.Error(w, "原客机磁盘或机器标识尚未就绪，暂时无法准备恢复入口；磁盘和安装记录已保留", http.StatusConflict)
			return
		}
		s.launch(w, "macos-install-recovery", func() error {
			if err := guestos.CreateMacOSInstallRecovery(s.profile, credential.Username, install.ID); err != nil {
				return err
			}
			cmd := exec.CommandContext(s.operationContext(), s.exe, "guest-os", "macos", "stop")
			cmd.Env = append(os.Environ(), cage.ConfigEnv+"="+s.configPath)
			cmd.Stdout, cmd.Stderr = s.jobs.execution(), s.jobs.execution()
			return cmd.Run()
		})
		return
	}
	if r.Method == http.MethodGet {
		jsonReply(w, map[string]any{"available": true, "phase": recovery.Phase, "running": macOSInstanceRunning(filepath.Join(s.profile, "state", "lima", "mac"))})
		return
	}
	var input guestos.MacLoginRequest
	if !readJSON(w, r, &input) {
		return
	}
	switch input.Action {
	case "start":
		s.launch(w, "macos-install-recovery", func() error {
			cmd := exec.CommandContext(s.operationContext(), s.exe, "guest-os", "macos", "start")
			cmd.Env = append(os.Environ(), cage.ConfigEnv+"="+s.configPath)
			cmd.Stdout, cmd.Stderr = s.jobs.execution(), s.jobs.execution()
			return cmd.Run()
		})
		return
	case "resume":
		s.beginGuestInstallMode(w, r, guestInstallRequest{System: guestos.MacOS}, true, recovery.User)
		return
	case "open", "close", "frame", "click", "key", "scroll":
	default:
		http.Error(w, "恢复画面操作无效", http.StatusBadRequest)
		return
	}
	input.Setup = true
	input.Maintenance = ""
	input.Password = ""
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var dial net.Dialer
	conn, err := dial.DialContext(ctx, "unix", filepath.Join(s.profile, "state", "lima", "mac", "cage-login.sock"))
	if err != nil {
		s.recordInstallRecovery("连接恢复画面失败：" + err.Error())
		http.Error(w, "请先启动恢复客机", http.StatusConflict)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if err := json.NewEncoder(conn).Encode(input); err != nil {
		s.recordInstallRecovery(fmt.Sprintf("发送恢复画面请求失败：%v", err))
		http.Error(w, "恢复画面无法响应", http.StatusServiceUnavailable)
		return
	}
	var result guestos.MacLoginReply
	if err := json.NewDecoder(io.LimitReader(conn, 6<<20)).Decode(&result); err != nil {
		s.recordInstallRecovery(fmt.Sprintf("读取恢复画面响应失败：%v", err))
		http.Error(w, "恢复画面无法响应", http.StatusServiceUnavailable)
		return
	}
	if input.Action == "close" || result.Closed {
		s.mu.Lock()
		lease := s.macLoginLease
		matching := lease != nil && lease.request.Session == input.Session
		s.mu.Unlock()
		if matching && s.jobs.snapshot().Kind == "guest-install" {
			s.jobs.cancelRunning()
		}
	}
	epoch, _ := s.account.access()
	// lifetimeContext takes s.mu; resolve it before the lease critical section
	// below takes the same lock.
	lifetime := s.lifetimeContext()
	if input.Action == "open" && result.Session != "" {
		lease := &macLoginLease{request: guestos.MacLoginRequest{Action: "close", Session: result.Session, Setup: true}, socket: filepath.Join(s.profile, "state", "lima", "mac", "cage-login.sock")}
		s.mu.Lock()
		previous := s.macLoginLease
		s.macLoginLease = lease
		lease.stopAccount = context.AfterFunc(epoch, func() { s.revokeMacLogin(result.Session) })
		lease.stopService = context.AfterFunc(lifetime, func() { s.revokeMacLogin(result.Session) })
		s.mu.Unlock()
		previous.stop()
	} else if input.Action == "close" || result.Closed {
		s.trackMacLogin(epoch, input, result)
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonReply(w, result)
}
