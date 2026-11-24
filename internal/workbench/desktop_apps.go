package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

//go:embed linux-app-desktop.sh
var linuxAppDesktop string

//go:embed linux-app-install.sh
var linuxAppInstall string

//go:embed linux-app-remove.sh
var linuxAppRemove string

// desktopAppResolverLimit is the largest resolver program a plugin may have.
const desktopAppResolverLimit = 256 << 10

// desktopAppInstance is the environment's name for one plugin application.
// It has the form of a Desktop instance, so the display, clipboard, file
// chooser and downloads that serve Desktop instances serve it too, and it
// carries neither the plugin's name nor the product's.
func desktopAppInstance(plugin, app string) string {
	sum := sha256.Sum256([]byte("desktop-app\x00" + plugin + "\x00" + app))
	return hex.EncodeToString(sum[:12])
}

type desktopAppTarget struct {
	plugin string
	dir    string
	app    hostbridge.DesktopApp
	id     string
}

// desktopAppTarget finds an application of an enabled plugin that this
// version of the application can run.
func (s *Server) desktopAppTarget(plugin, app string) (desktopAppTarget, bool) {
	manifest, ok := s.hostViewManifest(plugin)
	if !ok || manifest.Kind != hostbridge.KindGuest || manifest.Contributes == nil {
		return desktopAppTarget{}, false
	}
	for _, candidate := range manifest.Contributes.DesktopApps {
		if candidate.ID == app {
			return desktopAppTarget{
				plugin: plugin,
				dir:    filepath.Join(s.hostViewProfile(), "plugins", plugin),
				app:    candidate,
				id:     desktopAppInstance(plugin, app),
			}, true
		}
	}
	return desktopAppTarget{}, false
}

// desktopAppVariables are the installer's inputs, as shell assignments of
// values the manifest check has already limited.
func desktopAppVariables(t desktopAppTarget) (string, error) {
	command, err := json.Marshal(t.app.Command)
	if err != nil {
		return "", err
	}
	source := t.app.Source
	resolver, kind, args := "", "", "[]"
	if len(source.Resolve) > 0 {
		program, err := os.ReadFile(filepath.Join(t.dir, filepath.FromSlash(source.Resolve[0])))
		if err != nil {
			return "", fmt.Errorf("读取插件的版本解析程序：%w", err)
		}
		if len(program) > desktopAppResolverLimit {
			return "", errors.New("插件的版本解析程序过大")
		}
		encoded, err := json.Marshal(source.Resolve[1:])
		if err != nil {
			return "", err
		}
		resolver, kind, args = base64.StdEncoding.EncodeToString(program), "sh", string(encoded)
		if strings.HasSuffix(strings.ToLower(source.Resolve[0]), ".py") {
			kind = "py"
		}
	}
	values := [][2]string{
		{"app_id", t.id},
		{"app_source", source.Type},
		{"app_command", string(command)},
		{"app_schemes", strings.Join(t.app.Schemes, " ")},
		{"app_packages", strings.Join(t.app.Packages, " ")},
		{"app_dependencies", cage.DesktopAppDependencies},
		{"app_repository", source.Repository},
		{"app_suite", source.Suite},
		{"app_components", strings.Join(source.Components, " ")},
		{"app_key", source.Key},
		{"app_fingerprint", source.Fingerprint},
		{"app_package", source.Package},
		{"app_hosts", strings.Join(source.Hosts, " ")},
		{"app_resolver", resolver},
		{"app_resolver_kind", kind},
		{"app_resolver_args", args},
	}
	var b strings.Builder
	for _, value := range values {
		b.WriteString(value[0] + "=" + shellescape.Quote(value[1]) + "\n")
	}
	return b.String(), nil
}

func desktopAppInstallScript(t desktopAppTarget) (string, error) {
	variables, err := desktopAppVariables(t)
	if err != nil {
		return "", err
	}
	return cage.ToolInstallPrelude() + cage.DesktopAppSupportScript() + variables + linuxAppInstall, nil
}

func desktopAppRemoveScript(t desktopAppTarget) string {
	return "set -euo pipefail\napp_id=" + t.id + "\n" + linuxAppRemove
}

type desktopAppState struct {
	ID        string `json:"id"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Previous  string `json:"previous_version,omitempty"`
	Running   bool   `json:"running"`
	Failed    bool   `json:"failed"`
	// FailureReason is oom-kill when the system ended the application to free memory.
	FailureReason string `json:"failure_reason,omitempty"`
}

const desktopAppSnapshot = `python3 -I - %s <<'APP_STATE'
import json, subprocess, sys
from pathlib import Path
instance = sys.argv[1]
def run(*args):
    return subprocess.run(args, capture_output=True, text=True).stdout.strip()
record = {}
try:
    record = json.loads(Path('/usr/local/lib/desktop-apps/' + instance + '/app.json').read_text())
except (OSError, ValueError):
    pass
unit = 'desktop-' + instance + '.service'
state = run('systemctl', 'is-active', unit)
result = run('systemctl', 'show', '--property=Result', '--value', unit) if state == 'failed' else ''
print(json.dumps({'id': instance, 'installed': bool(record.get('command')), 'version': record.get('version', ''),
    'previous_version': record.get('previous_version', ''), 'running': state == 'active', 'failed': state == 'failed',
    'failure_reason': 'oom-kill' if result == 'oom-kill' else ''}))
APP_STATE`

// desktopApp serves one plugin application: its state, the operations on it
// and, while it runs, its display, clipboard and input method.
func (s *Server) desktopApp(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	get := map[string]bool{"status": true, "vnc": true, "vnc-credentials": true, "clipboard": true, "input": true}
	post := map[string]bool{"install": true, "update": true, "remove": true, "start": true, "stop": true}
	if r.Method == http.MethodGet && !get[action] || r.Method == http.MethodPost && !post[action] {
		http.NotFound(w, r)
		return
	}
	target, ok := s.desktopAppTarget(r.PathValue("plugin"), r.PathValue("app"))
	if !ok {
		http.Error(w, "插件未启用，或这个版本的 Cage 还不能运行它的应用", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPost {
		var input struct{}
		if !readJSON(w, r, &input) {
			return
		}
	}
	if !s.accountDesktopsSupported() {
		http.Error(w, "此环境暂不支持插件桌面应用", http.StatusNotImplemented)
		return
	}
	if !s.vmRunning() {
		http.Error(w, "请先启动 Cage 环境", 409)
		return
	}
	run := "/run/desktop-" + target.id
	switch action {
	case "status":
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		b, err := s.guestQuick(ctx, fmt.Sprintf(desktopAppSnapshot, target.id))
		var state desktopAppState
		if err != nil || json.Unmarshal(b, &state) != nil || state.ID != target.id {
			http.Error(w, "读取应用状态失败，请确认环境仍在运行", http.StatusBadGateway)
			return
		}
		jsonReply(w, state)
		return
	case "vnc":
		s.streamDesktopSocket(w, r, run+"/vnc.sock")
		return
	case "vnc-credentials":
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		b, err := s.guest(ctx, "systemctl is-active --quiet desktop-"+target.id+".service && cat "+run+`/password && { if test -S "$HOME/.local/share/desktop-apps/`+target.id+`/input/.cache/cage-input/input.sock"; then printf 1; else printf 0; fi; }`)
		if err != nil || len(b) != 9 || (b[8] != '0' && b[8] != '1') {
			http.Error(w, "应用尚未打开", 409)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		jsonReply(w, map[string]any{"password": string(b[:8]), "native_input": b[8] == '1'})
		return
	case "clipboard":
		s.sharedDesktopClipboard(w, r, target.id)
		return
	case "input":
		s.streamDesktopCommand(w, r, `exec env HOME="$HOME/.local/share/desktop-apps/`+target.id+`/input" /usr/local/bin/cage-native-input --relay`)
		return
	}
	s.launchRequest(w, r, "desktop-app-"+action, func() error {
		return s.changeDesktopApp(target, action)
	})
}

func (s *Server) changeDesktopApp(t desktopAppTarget, action string) error {
	title := t.app.Title
	switch action {
	case "install", "update":
		s.mu.Lock()
		allowed := s.toolsNetworkAllowedLocked()
		tls12Compat := s.preferences.InstallTLS12Compat
		s.mu.Unlock()
		if !allowed {
			return errors.New("工作台正在退出，请稍后重试安装")
		}
		script, err := desktopAppInstallScript(t)
		if err != nil {
			return err
		}
		if action == "update" {
			s.setMessage("正在把 " + title + " 更新到官方最新版本，可在操作详情查看进度…")
			script = "set -e\nsudo systemctl stop desktop-" + t.id + ".service 2>/dev/null || :\n" + cage.InstallTLS12Prelude(tls12Compat) + script
		} else {
			s.setMessage("正在下载并安装 " + title + "，可在操作详情查看进度…")
			script = cage.InstallTLS12Prelude(tls12Compat) + script
		}
		return RunMaintenanceScript(s.operationContext(), s.configPath, script, s.jobs.execution(), s.jobs.execution())
	case "remove":
		s.setMessage("正在卸载 " + title + "…")
		return s.runDesktopScript(desktopAppRemoveScript(t), 5*time.Minute)
	case "start":
		s.setMessage("正在打开 " + title + "…")
		if err := s.runDesktopScript(cage.DesktopAppSupportScript(), 30*time.Second); err != nil {
			return err
		}
		key, err := s.account.desktopKey()
		if err != nil {
			return err
		}
		defer clear(key)
		return s.runDesktopApp(t, "start", key)
	default:
		s.setMessage("正在关闭 " + title + "…")
		return s.runDesktopApp(t, "stop", nil)
	}
}

func (s *Server) runDesktopApp(t desktopAppTarget, action string, key []byte) error {
	ctx, cancel := context.WithTimeout(s.operationContext(), 100*time.Second)
	defer cancel()
	command := fmt.Sprintf("exec /bin/bash -c %s -- %s %s", shellescape.Quote(linuxAppDesktop), t.id, action)
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
	cmd.Stdin = bytes.NewReader(key)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		_, _ = s.jobs.execution().Write(output.Bytes())
		if bytes.Contains(output.Bytes(), []byte("应用尚未安装")) {
			return errors.New("请先安装 " + t.app.Title)
		}
		if bytes.Contains(output.Bytes(), []byte("应用程序缺失")) {
			return errors.New(t.app.Title + " 的程序缺失，请点击“更新 / 修复安装”")
		}
		return executionFailure("操作 "+t.app.Title, errors.Join(ctx.Err(), err), s.jobs.snapshot().Log)
	}
	return nil
}
