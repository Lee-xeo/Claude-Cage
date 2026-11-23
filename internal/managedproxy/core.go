package managedproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/host"
)

// Preserve causes for errors.Is/As without exposing private socket paths,
// request URLs, credentials or response contents in displayed/logged errors.
type coreError struct {
	stage  string
	err    error
	status int // HTTP status from the core API, 0 when no response arrived
}

func (e *coreError) Error() string {
	message := "Cage 代理核心操作失败（" + e.stage + "）"
	var nested *coreError
	var errno syscall.Errno
	var network net.Error
	switch {
	case e.err == nil:
		return message
	case errors.As(e.err, &nested):
		return message + ": " + nested.Error()
	case errors.Is(e.err, context.Canceled):
		return message + ": context canceled"
	case errors.Is(e.err, context.DeadlineExceeded):
		return message + ": context deadline exceeded"
	case errors.As(e.err, &errno):
		return fmt.Sprintf("%s: errno %d", message, errno)
	case errors.As(e.err, &network) && network.Timeout():
		return message + ": timeout"
	default:
		return fmt.Sprintf("%s: %T", message, e.err)
	}
}

func (e *coreError) Unwrap() error { return e.err }

// CorePath deliberately never searches PATH or another application's bundle.
func CorePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "mihomo"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, p := range []string{filepath.Join(filepath.Dir(exe), name), filepath.Join(filepath.Dir(exe), "..", "proxy-core", name)} {
		if st, e := os.Stat(p); e == nil && st.Mode().IsRegular() && (runtime.GOOS == "windows" || st.Mode()&0111 != 0) {
			return filepath.Clean(p), nil
		}
	}
	return "", missingCoreError(name, runtime.GOOS)
}

// missingCoreError names where CorePath looks, without the host path.
func missingCoreError(name, goos string) error {
	build := "源码构建请运行 make cage-core"
	if goos == "windows" {
		build = "源码构建请运行 scripts/build-windows.ps1"
	}
	return fmt.Errorf("Cage 随附代理核心缺失：%s 应与 cage 可执行文件位于同一目录，或位于其上级目录的 proxy-core 中；请重新安装完整应用，%s", name, build)
}
func Validate(ctx context.Context, profile string, s Store) error {
	core, err := CorePath()
	if err != nil {
		return err
	}
	b, err := s.Runtime(profile)
	if err != nil {
		return err
	}
	dir := Dir(profile)
	if err = host.EnsurePrivateDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "validate-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = host.ProtectFile(f.Name()); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	f.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, core, "-t", "-d", dir, "-f", f.Name())
	cmd.Env = coreEnvironment(dir)
	output := diagnostics.New(profile).Stream("proxy-validation", "error")
	defer output.Close()
	cmd.Stdout, cmd.Stderr = output, output
	if e := cmd.Run(); e != nil {
		return errors.New("随附的 Mihomo 核心未通过配置校验，请核对节点协议和参数；可在设置的“日志与诊断”中导出诊断包查看校验记录；原配置已保留")
	}
	return nil
}
func API(ctx context.Context, profile, method, path string, body any, reply any) error {
	tr := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", Socket(profile))
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return &coreError{stage: "api.encode", err: err}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://cage-proxy"+path, bytes.NewReader(data))
	if err != nil {
		return &coreError{stage: "api.request", err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return &coreError{stage: "api.transport", err: err}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &coreError{stage: fmt.Sprintf("api.http-%d", res.StatusCode), status: res.StatusCode}
	}
	if reply != nil {
		if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(reply); err != nil {
			return &coreError{stage: "api.decode", err: err}
		}
	}
	return nil
}

// DelayError explains a failed /proxies/{id}/delay call. Checked against the
// bundled mihomo v1.19.33 hub/route: 404 when the node is not loaded
// (findProxyByName), 504 when the test timed out, 503 when the node failed to
// connect or respond, 400 for an invalid query. 408 is not sent by that version
// and is treated as a timeout only for forward compatibility. Only fixed text and the API stage are
// reported; node addresses and core output stay out of the message.
func DelayError(err error) error {
	var core *coreError
	if !errors.As(err, &core) {
		return fmt.Errorf("测速失败：%w", err)
	}
	switch {
	case core.status == http.StatusNotFound:
		return errors.New("节点未载入核心；请先应用节点以载入当前列表")
	case core.status == http.StatusGatewayTimeout || core.status == http.StatusRequestTimeout:
		return errors.New("测速超时：节点已载入核心，但 5 秒内没有收到测速地址的响应；请检查节点或上游代理")
	case core.status == http.StatusServiceUnavailable:
		return errors.New("测速失败：节点已载入核心，但连接节点或上游代理时出错；请检查节点或上游代理")
	case core.status != 0:
		return fmt.Errorf("测速失败：代理核心返回 HTTP %d", core.status)
	case strings.HasPrefix(core.stage, "api.transport"):
		return fmt.Errorf("代理核心未运行或无法连接；请先应用节点。%w", err)
	default:
		return fmt.Errorf("测速失败：%w", err)
	}
}

func Running(ctx context.Context, profile string, s Store) bool {
	return RunningStage(ctx, profile, s) == ""
}

// RunningStage reports a fixed diagnostic stage code when the managed proxy
// core is not ready for the selected route. It never includes node addresses,
// credentials, headers, or host paths.
func RunningStage(ctx context.Context, profile string, s Store) string {
	return runningStageError(checkRunning(ctx, profile, s))
}

func runningStageError(err error) string {
	if err == nil {
		return ""
	}
	var core *coreError
	if errors.As(err, &core) && core.stage != "" {
		if errors.Is(err, context.DeadlineExceeded) {
			return core.stage + ".timeout"
		}
		return core.stage
	}
	if errors.Is(err, ErrLocalProxyNode) {
		return "route.local_node"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "route"
}

func checkRunning(ctx context.Context, profile string, s Store) error {
	if _, err := s.Route(); err != nil {
		return err
	}
	var cfg struct {
		Port         int    `json:"mixed-port"`
		Interface    string `json:"interface-name"`
		UnifiedDelay bool   `json:"unified-delay"`
	}
	var group struct {
		Now string `json:"now"`
	}
	bound, err := BoundInterface(filepath.Join(Dir(profile), "config.yaml"))
	if err != nil || bound == "" {
		return &coreError{stage: "readiness.bound-interface", err: err}
	}
	if err := API(ctx, profile, "GET", "/configs", nil, &cfg); err != nil {
		return &coreError{stage: "readiness.configs", err: err}
	}
	if cfg.Port != s.Port {
		return &coreError{stage: "readiness.port"}
	}
	if cfg.Interface != bound {
		return &coreError{stage: "readiness.interface"}
	}
	if !cfg.UnifiedDelay {
		return &coreError{stage: "readiness.unified-delay"}
	}
	if err := API(ctx, profile, "GET", "/proxies/"+Group, nil, &group); err != nil {
		return &coreError{stage: "readiness.group", err: err}
	}
	if group.Now != s.Selected {
		return &coreError{stage: "readiness.selection"}
	}
	return nil
}
func label(profile string) string {
	h := sha256.Sum256([]byte(profile))
	return fmt.Sprintf("local.claude-cage.managed-proxy.%x", h[:8])
}

// coreReadyTimeout matches Linux and Windows; a freshly signed core can take
// several seconds on its first launch.
const coreReadyTimeout = 15 * time.Second

// launchdStartArgs registers the per-profile job and then starts that job.
func launchdStartArgs(uid int, label, plist string) (bootstrap, kickstart []string) {
	domain := "gui/" + strconv.Itoa(uid)
	return []string{"bootstrap", domain, plist}, []string{"kickstart", domain + "/" + label}
}
func servicePath(profile string) string { return filepath.Join(Dir(profile), "managed-service.plist") }
func Stop(profile string) error {
	if runtime.GOOS == "windows" {
		return stopWindowsCore(profile)
	}
	if runtime.GOOS == "linux" {
		return stopLinuxCore(profile)
	}
	if _, err := os.Stat(servicePath(profile)); os.IsNotExist(err) {
		return nil
	}
	if runtime.GOOS != "darwin" {
		return errors.New("内置代理当前支持 macOS")
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if exec.Command("launchctl", "print", domain+"/"+label(profile)).Run() != nil {
		return nil
	}
	if exec.Command("launchctl", "bootout", domain, servicePath(profile)).Run() != nil {
		return errors.New("未能停止 Cage 专属代理进程")
	}
	return nil
}
func Start(ctx context.Context, profile string, s Store) error {
	if _, err := s.Route(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return startWindowsCore(ctx, profile, s)
	}
	if runtime.GOOS == "linux" {
		return startLinuxCore(ctx, profile, s)
	}
	if runtime.GOOS != "darwin" {
		return errors.New("内置代理当前支持 macOS")
	}
	name, err := PhysicalInterface(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(Dir(profile), "config.yaml")
	if BindingMatches(path, name) && Running(ctx, profile, s) {
		return nil
	}
	if err := Validate(ctx, profile, s); err != nil {
		return err
	}
	if err := Stop(profile); err != nil {
		return err
	}
	// Never attach to or kill an unknown listener, even if it uses our port.
	l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", s.Port))
	if err != nil {
		return errors.New("Cage 代理端口已被占用，请更换独立端口")
	}
	l.Close()
	_, err = CorePath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(Socket(profile)), 0700); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Dir(Socket(profile)), 0700); err != nil {
		return err
	}
	b, err := s.Runtime(profile)
	if err != nil {
		return err
	}
	b, err = BindInterface(b, name)
	if err != nil {
		return err
	}
	if err = Write(path, b); err != nil {
		return err
	}
	_ = os.Remove(Socket(profile))
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	configPath := os.Getenv("CLAUDE_CAGE_CONFIG")
	if configPath == "" {
		configPath = filepath.Join(profile, "config.json")
	}
	escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>log-relay</string><string>proxy</string></array><key>EnvironmentVariables</key><dict><key>HOME</key><string>%s</string><key>PATH</key><string>/usr/bin:/bin</string><key>CLAUDE_CAGE_CONFIG</key><string>%s</string></dict><key>RunAtLoad</key><true/></dict></plist>`, escape(label(profile)), escape(executable), escape(Dir(profile)), escape(configPath))

	if err = Write(servicePath(profile), []byte(plist)); err != nil {
		return err
	}
	bootstrap, kickstart := launchdStartArgs(os.Getuid(), label(profile), servicePath(profile))
	if exec.CommandContext(ctx, "launchctl", bootstrap...).Run() != nil {
		return errors.New("无法启动 Cage 自带代理核心")
	}
	// RunAtLoad is only a request: a GUI domain in on-demand-only mode keeps
	// the job pending. Start it explicitly; without -k a running job is kept.
	// The readiness wait below still decides whether the core is usable.
	if err := exec.CommandContext(ctx, "launchctl", kickstart...).Run(); err != nil {
		diagnostics.Log("warn", "proxy", "launchd kickstart failed; waiting for RunAtLoad")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(coreReadyTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = Stop(profile)
			return ctx.Err()
		case <-deadline.C:
			_ = Stop(profile)
			return errors.New("代理核心启动超时，请查看私有代理日志")
		case <-ticker.C:
			if Running(ctx, profile, s) {
				if err = os.Chmod(Socket(profile), 0600); err != nil {
					_ = Stop(profile)
					return err
				}
				return nil
			}
		}
	}
}
