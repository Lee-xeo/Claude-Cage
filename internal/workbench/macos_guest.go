package workbench

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/host"
)

// Only the trusted local workbench can operate the native Mac. Each route
// must have a native guest implementation or run entirely on the host.
func macOSWorkbenchRoute(path string) bool {
	if nativeWorkbenchRoute(path) {
		return true
	}
	if id, ok := strings.CutPrefix(path, "/api/input/files/desktop/"); ok {
		return id == "requests" || id == "open" || browserFileID.MatchString(id)
	}
	switch path {
	case "/api/macos/first-setup", "/api/appearance/codex", "/api/macos/project-approval", "/api/macos/exit-policy", "/api/macos/login", "/api/macos/maintenance":
		return true
	}
	return strings.HasPrefix(path, "/api/macos/claude/") || strings.HasPrefix(path, "/api/macos-browser/")
}

func macOSInstanceRunning(instanceDir, backend string) bool {
	data, err := os.ReadFile(filepath.Join(instanceDir, "cage-running.json"))
	var record struct {
		PID     int    `json:"pid"`
		Started string `json:"started"`
	}
	if err != nil || json.Unmarshal(data, &record) != nil || record.PID < 1 {
		return false
	}
	if !processExists(record.PID) || !cage.ProcessStartMatches(record.PID, record.Started) {
		return false
	}
	if backend == guestos.MacOSQEMU {
		path := filepath.Join(instanceDir, "qemu.pid")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 32 {
			return false
		}
		data, err := os.ReadFile(path)
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && parseErr == nil && pid > 1 && processExists(pid)
	}
	return true
}

func (s *Server) macOSSSHCommand(ctx context.Context, args ...string) *exec.Cmd {
	base, err := guestos.MacOSSSHArgs(s.profile, s.macosGuest.User)
	if err != nil {
		return &exec.Cmd{Err: err}
	}
	cmd := exec.CommandContext(ctx, sshExecutable(), append(base, args...)...)
	cmd.Env = sshEnvironment()
	host.HideCommandWindow(cmd)
	return cmd
}

func (s *Server) checkMacOSGuest(ctx context.Context) error {
	if !s.vmRunning() {
		return errors.New("macOS 客机尚未启动")
	}
	client, err := s.gateClient()
	if err != nil {
		return errors.New("无法验证 macOS 客机的网络隔离状态")
	}
	state, err := client.Status(ctx)
	if err != nil {
		return errors.New("无法验证 macOS 客机的网络门槛")
	}
	if state.Mode != cage.GateClosed {
		s.mu.Lock()
		ready, paused := s.ready, s.networkPaused
		s.mu.Unlock()
		reason := ""
		switch {
		case s.macSetupPending():
			reason = "首次系统设置期间外网关闭"
		case paused:
			reason = "网络保持暂停，请手动恢复后再联网"
		case !ready:
			reason = "客机重新连接，外网等待重新检查"
		default:
			cfg, cfgErr := loadConfig(s.configPath)
			if state.Mode != cage.GateOpen || cfgErr != nil || state.Report == nil || !time.Now().Before(state.Until) || state.Report.Authorizes(state.RunID, cfg.Fingerprint(), time.Now()) != nil {
				reason = "网络授权未通过当前配置的完整检查"
			}
		}
		if reason != "" {
			state, err = client.Close(ctx, reason)
			if err != nil || state.Mode != cage.GateClosed {
				return errors.New("macOS 客机网络状态不能确认")
			}
		}
	}
	s.publishGate(state)
	probe := `test "$(uname -s)" = Darwin && [[ $(sysctl -n hw.model) == VirtualMac* ]] && test "$(id -un)" = ` +
		shellescape.Quote(s.macosGuest.User) + ` && [[ " $(id -Gn) " != *" admin "* ]]`
	if s.macosGuest.Backend == guestos.MacOSQEMU {
		probe = `/usr/local/libexec/cage-guest-check --expect ` + shellescape.Quote(s.macosGuest.UUID) + ` && test "$(id -un)" = ` + shellescape.Quote(s.macosGuest.User) + ` && [[ " $(id -Gn) " != *" admin "* ]]`
	}
	if _, err := s.guest(ctx, probe); err != nil {
		return errors.New("无法以已绑定的普通用户连接到这台虚拟 Mac，请检查客机身份和 SSH 登录")
	}
	return nil
}

// macOSComponentReceiptProbe reads only the identity of the installed
// component receipt. Its file digests are checked by the explicit version view.
const macOSComponentReceiptProbe = `receipt=/var/db/cage-components.json
if ! test -e "$receipt"; then printf 'null'; exit 0; fi
exec /usr/local/libexec/cage-cli/bin/python3 -I -c '
import json, sys
try:
    with open(sys.argv[1]) as stream:
        value = json.load(stream)
    print(json.dumps({key: value.get(key) for key in ("protocol", "revision", "app_version", "system")}))
except (ValueError, AttributeError):
    print("null")
' "$receipt"`

var errMacOSComponentUpdate = errors.New("虚拟 Mac 的客机组件与当前 Cage 版本不一致，浏览器、剪贴板和声音可能无法连接。请保存工作后在环境设置中同步环境组件；系统磁盘、账户和项目数据会保留。")

// macOSComponentsDrifted reports whether a receipt read from the guest belongs
// to a different component kit. A missing or unreadable receipt also requires
// an update, matching the explicit version view.
func macOSComponentsDrifted(receipt []byte, required cage.GuestManifest) bool {
	var installed *struct {
		cage.GuestManifest
		System string `json:"system"`
	}
	if json.Unmarshal(receipt, &installed) != nil || installed == nil {
		return true
	}
	return installed.System != guestos.MacOS || installed.Protocol != required.Protocol || installed.Revision != required.Revision
}

// checkMacOSComponents runs once per connection. Without it a guest left on
// older components looks ready while its services fail, and every client
// retries those failures instead of offering the component update.
func (s *Server) checkMacOSComponents() {
	kit, system := s.nativeComponentKit()
	required, err := guestos.RequiredNativeComponents(kit, system)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), 10*time.Second)
	defer cancel()
	receipt, err := s.guest(ctx, macOSComponentReceiptProbe)
	if err != nil {
		return
	}
	drifted := macOSComponentsDrifted(receipt, required.GuestManifest)
	s.mu.Lock()
	s.macComponentsChecked = true
	s.guestUpdateRequired = drifted
	s.mu.Unlock()
}

func (s *Server) attachMacOSGuest() error {
	ctx, cancel := s.controlContext()
	defer cancel()
	err := s.checkMacOSGuest(ctx)
	s.mu.Lock()
	if err != nil {
		s.macComponentsChecked = false
	}
	checkComponents := err == nil && !s.macComponentsChecked
	s.mu.Unlock()
	if checkComponents && !s.macSetupPending() {
		s.checkMacOSComponents()
	}
	s.mu.Lock()
	newlyReady := !s.ready && err == nil
	checkAfterReconnect := newlyReady && !s.closing && !s.networkPaused && !s.jobs.snapshot().Running
	s.ready = err == nil
	s.guestUpdateError = ""
	if err != nil {
		s.guestUpdateError = err.Error()
	} else if s.guestUpdateRequired {
		s.guestUpdateError = errMacOSComponentUpdate.Error()
	}
	s.mu.Unlock()
	if newlyReady && !s.macSetupPending() {
		s.restoreProjectShares()
		if checkAfterReconnect {
			_, _ = s.submitJob("network-recovery", s.checkNetworkIfWanted)
		}
	} else if err == nil && !s.macSetupPending() {
		s.retryMacOSProjectShares(ctx)
	}
	return err
}

func (s *Server) macOSLifecycleCommand(action string) error {
	ctx, cancel := context.WithTimeout(s.operationContext(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.exe, "guest-os", "macos", action)
	cmd.Env = append(os.Environ(), cage.ConfigEnv+"="+s.configPath)
	job := s.jobs.execution()
	cmd.Stdout, cmd.Stderr = job, job
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("macOS 客机操作失败（%s）：%w", action, err)
	}
	return nil
}

func (s *Server) startMacOSGuest() error {
	if !s.vmRunning() {
		if err := s.macOSLifecycleCommand("start"); err != nil {
			return err
		}
	}
	if err := s.syncMacGuestPassword(s.operationContext()); err != nil {
		return err
	}
	if err := s.attachMacOSGuest(); err != nil {
		return err
	}
	return s.checkNetworkIfWanted()
}

//go:embed macos-locale.sh
var macOSLocaleScript string

func (s *Server) syncMacOSRegion(ctx context.Context, cfg cage.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.setMessage("正在应用 macOS 客机账户的地区与语言…")
	locale := strings.TrimSuffix(cfg.Locale, ".UTF-8")
	cmd := shellescape.QuoteCommand([]string{"/bin/bash", "-c", macOSLocaleScript, "cage-locale", "NSGlobalDomain", locale})
	if _, err := s.guest(ctx, "exec "+cmd); err != nil {
		return fmt.Errorf("macOS 客机已启动，未能写入客机账户的地区与语言：%w", err)
	}
	s.setMessage("正在等候 macOS 客机读回已保存的时区与语言…")
	var last guestObservation
	for {
		got, err := s.macOSGuestObservation(ctx, map[string]any{"stage": "environment"})
		if err == nil {
			last = got
			if s.guestSettingsOK(got, cfg) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return ctx.Err()
			}
			return fmt.Errorf("macOS 客机的时区和语言尚未全部生效。期望 %s、%s，系统读回时区 %q、地区 %q、首选语言 %q，进程读回 %q、%q。外网保持关闭。可稍后重检，时区持续不符时请停止并重新启动客机。", cfg.Timezone, cfg.Locale, last.SystemTimezone, last.SystemLocale, last.SystemLanguage, last.TZ, last.ProcessLocale)
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) stopMacOSGuest() error {
	s.mu.Lock()
	s.macSetupProjectsChecked = time.Time{}
	s.mu.Unlock()
	s.closeWindowsBrowser()
	s.mu.Lock()
	s.ready = false
	s.mu.Unlock()
	s.terminals.closeAll()
	s.closeProjectShares()
	return s.macOSLifecycleCommand("stop")
}
