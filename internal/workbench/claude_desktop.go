package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

type claudeDesktopState struct {
	Installed         bool    `json:"installed"`
	Version           string  `json:"version"`
	Running           bool    `json:"running"`
	Failed            bool    `json:"failed"`
	Managed           bool    `json:"managed"`
	NativeUserSession bool    `json:"native_user_session,omitempty"`
	DisplayScale      float64 `json:"display_scale,omitempty"`
	FailureReason     string  `json:"failure_reason,omitempty"`
}

const claudeDesktopSnapshot = `python3 - <<'CAGE_DESKTOP_STATE'
import json, subprocess
from pathlib import Path
def run(*args):
    return subprocess.run(args, capture_output=True, text=True).stdout.strip()
package=run('dpkg-query','-W','-f=${Status}\n${Version}','claude-desktop').splitlines()
installed=bool(package and package[0]=='install ok installed')
state=run('systemctl','is-active','cage-claude-desktop.service')
result=run('systemctl','show','--property=Result','--value','cage-claude-desktop.service') if state=='failed' else ''
print(json.dumps({'installed':installed,'version':package[1] if installed else '', 'running':state=='active','failed':state=='failed','managed':Path('/run/cage-desktop/keyring-ready').exists(),'failure_reason':'oom-kill' if result=='oom-kill' else ''}))
CAGE_DESKTOP_STATE`

func (s *Server) claudeDesktopStatus(w http.ResponseWriter, r *http.Request) {
	if !s.vmRunning() {
		http.Error(w, "请先启动 Cage 环境", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	b, err := s.desktopStatus.read(ctx, s.lifetimeContext(), func(ctx context.Context) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		return s.guestQuick(ctx, claudeDesktopSnapshot)
	})
	var state claudeDesktopState
	if err != nil {
		_ = diagnostics.New(s.profile).Record("warn", "claude-desktop", fmt.Sprintf("read state: %v", err))
		http.Error(w, usererror.Wrap("读取 Claude Desktop 状态", err, "请确认环境仍在运行；重新连接后再打开应用。持续失败时导出诊断包。").Error(), 502)
		return
	}
	if json.Unmarshal(b, &state) != nil {
		http.Error(w, "读取 Claude Desktop 状态失败：环境返回的应用状态格式无效。请更新环境组件；仍失败时导出诊断包。", 502)
		return
	}
	jsonReply(w, state)
}

func (s *Server) claudeDesktopAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "install" && action != "update" && action != "start" && action != "stop" {
		http.NotFound(w, r)
		return
	}
	var input struct{}
	if !readJSON(w, r, &input) {
		return
	}
	if !s.vmRunning() {
		http.Error(w, "请先启动 Cage 环境", 409)
		return
	}
	s.launchRequest(w, r, "claude-desktop-"+action, func() error {
		s.desktopStatus.invalidate()
		defer s.desktopStatus.invalidate()
		switch action {
		case "install", "update":
			s.mu.Lock()
			allowed := s.toolsNetworkAllowedLocked()
			s.mu.Unlock()
			if !allowed {
				return errors.New("工作台正在退出，请稍后重试安装")
			}
			monitorContext, stopMonitor := context.WithCancel(s.operationContext())
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-monitorContext.Done():
						return
					case <-ticker.C:
						s.monitorSafety()
					}
				}
			}()
			defer func() {
				stopMonitor()
				s.mu.Lock()
				if s.monitorCancel != nil {
					s.monitorCancel()
				}
				s.mu.Unlock()
				<-done
			}()
			s.setMessage("正在下载并安装 Claude Desktop，可在操作详情查看进度…")
			s.mu.Lock()
			tls12Compat := s.preferences.InstallTLS12Compat
			s.mu.Unlock()
			script := cage.InstallTLS12Prelude(tls12Compat) + cage.ClaudeDesktopInstallScript()
			if action == "update" {
				script = "set -e\nsudo systemctl stop cage-claude-desktop.service\n" + script
			}
			return RunMaintenanceScript(s.operationContext(), s.configPath, script, s.jobs.execution(), s.jobs.execution())
		case "start":
			return s.startClaudeDesktop()
		default:
			s.setMessage("正在关闭 Claude Desktop…")
			return s.runDesktopScript("sudo systemctl stop cage-claude-desktop.service", 30*time.Second)
		}
	})
}

func (s *Server) startClaudeDesktop() error {
	defer func() { s.desktopViews.reset(); s.desktopViews.background(s.changeDesktopVisibility) }()
	s.desktopStatus.invalidate()
	defer s.desktopStatus.invalidate()
	s.setMessage("正在打开 Claude Desktop…")
	key, err := s.account.desktopKey()
	if err != nil {
		return err
	}
	defer clear(key)
	scope, err := s.defaultDesktopScope(s.operationContext())
	if err != nil {
		return err
	}
	script := `test "$(dpkg-query -W -f='${Status}' claude-desktop 2>/dev/null)" = 'install ok installed' || { echo '请先安装 Claude Desktop。'; exit 1; }
` + scope + cage.ClaudeDesktopProvisionScript()
	if s.hostToolsServing() {
		script += "\nmkdir -p \"$HOME/.config/Claude\"\n"
	}
	if err = s.runDesktopScript(script, 20*time.Second); err != nil {
		return err
	}
	s.syncHostToolsConfigNow(s.operationContext())
	ctx, cancel := context.WithTimeout(s.operationContext(), 55*time.Second)
	defer cancel()
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", "/usr/local/bin/cage-claude-start")
	cmd.Stdin = bytes.NewReader(key)
	cmd.Stdout, cmd.Stderr = s.jobs.execution(), s.jobs.execution()
	if err = cmd.Run(); err != nil {
		return executionFailure("打开 Claude Desktop", errors.Join(ctx.Err(), err), s.jobs.snapshot().Log)
	}
	s.watchDesktopLogins()
	return nil
}

func (s *Server) runDesktopScript(script string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(s.operationContext(), timeout)
	defer cancel()
	// A guest cgroup owns apt and all descendants, including across SSH loss.
	// The application's persistent service is separate from this operation.
	command := fmt.Sprintf("sudo systemd-run --quiet --pipe --wait --collect --unit=cage-desktop-operation --property=User=cage --property=RuntimeMaxSec=%d --property=KillMode=control-group --setenv=HOME=/home/cage --working-directory=/home/cage /bin/bash -se", int(timeout.Seconds()))
	var renderErr error
	command, renderErr = s.linuxTemplate(command)
	if renderErr != nil {
		return renderErr
	}
	script, renderErr = s.linuxTemplate(script)
	if renderErr != nil {
		return renderErr
	}
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout, cmd.Stderr = s.jobs.execution(), s.jobs.execution()
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			cleanup, finish := context.WithTimeout(context.Background(), 5*time.Second)
			defer finish()
			_, _ = s.guest(cleanup, "sudo systemctl stop cage-desktop-operation.service")
			return ctx.Err()
		}
		return executionFailure("准备或关闭 Claude Desktop", err, s.jobs.snapshot().Log)
	}
	return nil
}

func (s *Server) claudeDesktopCredentials(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	b, err := s.guestQuick(ctx, `set -eu
systemctl is-active --quiet cage-claude-desktop.service
password=$(cat "$HOME/.config/claude-cage/desktop-password")
printf '%s\n' "$password"
if test -f /run/cage-desktop/base-display-scale; then cat /run/cage-desktop/base-display-scale
elif test -f /run/cage-desktop/display-scale; then cat /run/cage-desktop/display-scale
else echo; fi
if test -f /run/cage-desktop/xsettings.pid; then echo dynamic; fi`)
	password, scale, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if err != nil || len(password) != 8 {
		http.Error(w, "Claude Desktop 尚未启动", 409)
		return
	}
	// With a settings manager the viewer chooses the density of its own screen.
	scale, dynamic, _ := strings.Cut(scale, "\n")
	density := 2.0
	if scale == "1.5" {
		density = 1.5
	}
	jsonReply(w, map[string]any{"password": password, "display_scale": density, "dynamic_scale": dynamic == "dynamic"})
}

// The zero value is a viewer that leaves the display's density alone.
func validDisplayDensity(scale float64) bool {
	return scale == 0 || scale >= 0.75 && scale <= 4
}

// A viewer's request for its pane: pixels, and the density to render them at.
type displaySize struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Scale  float64 `json:"scale"`
}

// readDisplaySize validates the request and formats its density for the guest
// script: empty for a viewer that leaves the display's density alone.
func readDisplaySize(w http.ResponseWriter, r *http.Request) (displaySize, string, bool) {
	var input displaySize
	if !readJSON(w, r, &input) {
		return input, "", false
	}
	if input.Width < 320 || input.Height < 240 || input.Width > 8192 || input.Height > 8192 || !validDisplayDensity(input.Scale) {
		http.Error(w, "画面尺寸无效", 400)
		return input, "", false
	}
	scale := ""
	if input.Scale != 0 {
		scale = strconv.FormatFloat(input.Scale, 'f', -1, 64)
	}
	return input, scale, true
}

// The reply names the density only when the display applied the requested
// one; a fixed-density display leaves the viewer to size for its own.
func displayResizeReply(w http.ResponseWriter, input displaySize, scale string, output []byte) {
	reply := map[string]any{"ok": true}
	if applied, _, _ := strings.Cut(string(output), "\n"); scale != "" && strings.TrimSpace(applied) == "scale "+scale {
		reply["scale"] = input.Scale
	}
	jsonReply(w, reply)
}

func (s *Server) claudeDesktopResize(w http.ResponseWriter, r *http.Request) {
	input, scale, ok := readDisplaySize(w, r)
	if !ok {
		return
	}
	script := desktopResizeScript(input.Width, input.Height, scale)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	output, err := s.guestQuick(ctx, script)
	if err != nil {
		http.Error(w, "Claude Desktop 画面尺寸未能调整", 502)
		return
	}
	displayResizeReply(w, input, scale, output)
}

// Fixed commands and validated numbers only; no host shell executes input.
// scale is empty for a viewer that leaves the display's density alone.
func desktopResizeScript(wid, hei int, scale string) string {
	const run = "/run/cage-desktop"
	return displayResizeScript(run, "export DISPLAY=:78 XAUTHORITY="+run+"/Xauthority\n",
		fmt.Sprintf(`printf '%%s %%s %%s\n' %d %d "$scale" > `+run+`/desktop-size
# A phone holds the display. Its release applies this size and density.
if test -f /run/cage-phone-desktop/layout.json; then exit 0; fi
`, wid, hei), "", wid, hei, scale)
}

// displayResizeScript applies a viewer's size and density to the display
// whose runtime directory is run. prelude locates the display; hold runs once
// the request is decided and may defer it; enter, when set, is the command
// prefix that runs the mode commands (read from stdin) where the display is
// reachable.
func displayResizeScript(run, prelude, hold, enter string, wid, hei int, scale string) string {
	modes := fmt.Sprintf(`name=cage-%dx%d
if ! xrandr --query | grep -q " $name "; then
  xrandr --newmode "$name" %.3f %d %d %d %d %d %d %d %d +HSync -VSync
  xrandr --addmode VNC-0 "$name"
fi
xrandr --output VNC-0 --mode "$name"
for old in $(xrandr --query | awk '/^ +cage-/ {print $1}'); do
  if [ "$old" != "$name" ]; then xrandr --delmode VNC-0 "$old"; xrandr --rmmode "$old"; fi
done
`, wid, hei, float64((wid+32)*(hei+8)*60)/1000000, wid, wid+8, wid+16, wid+32, hei, hei+1, hei+2, hei+8)
	if enter != "" {
		modes = enter + " <<'CAGE_DISPLAY_MODES'\n" + modes + "CAGE_DISPLAY_MODES\n"
	}
	return "set -eu\n" + prelude + "exec 9>" + run + "/resize.lock\nflock 9\nrequested=" + scale + `
manager=
if test -r ` + run + `/xsettings.pid; then
  manager=$(cat ` + run + `/xsettings.pid)
  if test "$(cat "/proc/$manager/comm" 2>/dev/null)" != xsettingsd; then manager=; fi
fi
# A fixed-density session ignores the request, and its viewer sizes for that
# density. A viewer that names none sized its display for the configured one.
scale=
if test -n "$manager"; then
  scale=${requested:-$(cat ` + run + `/base-display-scale 2>/dev/null || echo 2)}
  if test -n "$requested"; then echo "scale $scale"; fi
fi
` + hold + `if test -n "$manager" && test "$(cat ` + run + `/display-scale 2>/dev/null)" != "$scale"; then
  dpi=$(awk -v scale="$scale" 'BEGIN {printf "%.0f", scale * 96 * 1024}')
  # A session started by an earlier version has no list of its other settings.
  { printf 'Xft/DPI %s\nGdk/WindowScalingFactor 1\nGdk/UnscaledDPI %s\n' "$dpi" "$dpi"
    cat ` + run + `/xsettings.fixed 2>/dev/null || printf 'Gtk/DecorationLayout ":"\n'; } > ` + run + `/xsettings.conf.new
  mv ` + run + `/xsettings.conf.new ` + run + `/xsettings.conf
  kill -HUP "$manager"
  printf '%s\n' "$scale" > ` + run + `/display-scale
fi
` + modes
}

func (s *Server) claudeDesktopVNC(w http.ResponseWriter, r *http.Request) {
	if !s.vmRunning() {
		http.Error(w, "环境尚未启动", 409)
		return
	}
	release, err := s.acquireDesktopView()
	if err != nil {
		http.Error(w, "无法恢复 Claude Desktop 画面，请重新连接", http.StatusBadGateway)
		return
	}
	defer release()
	s.streamDesktopSocket(w, r, "/run/cage-desktop/vnc.sock")
}

func (s *Server) streamDesktopSocket(w http.ResponseWriter, r *http.Request, socket string) {
	s.streamDesktopCommand(w, r, "exec socat STDIO UNIX-CONNECT:"+socket)
}

func (s *Server) streamDesktopCommand(w http.ResponseWriter, r *http.Request, command string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// No extra localhost port or public VNC listener on the host. SSH is bound
	// to this profile's single guest and carries the authenticated RFB session.
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
	reader, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "无法连接应用画面", 502)
		return
	}
	writer, err := cmd.StdinPipe()
	if err != nil {
		_ = reader.Close()
		http.Error(w, "无法连接应用画面", 502)
		return
	}
	if err = cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		http.Error(w, "无法连接应用画面", 502)
		return
	}
	defer func() { cancel(); writer.Close(); reader.Close(); _ = cmd.Wait() }()
	s.streamVNC(w, r, reader, writer, func() { cancel(); _ = writer.Close(); _ = reader.Close() })
}
