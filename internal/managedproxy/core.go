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
	"syscall"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/host"
)

// Preserve causes for errors.Is/As without exposing private socket paths,
// request URLs, credentials or response contents in displayed/logged errors.
type coreError struct {
	stage string
	err   error
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
	return "", errors.New("Cage 随附代理核心缺失，请重新安装完整应用；开发构建请运行 scripts/fetch-proxy-core.sh")
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
		return &coreError{stage: fmt.Sprintf("api.http-%d", res.StatusCode)}
	}
	if reply != nil {
		if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(reply); err != nil {
			return &coreError{stage: "api.decode", err: err}
		}
	}
	return nil
}
func Running(ctx context.Context, profile string, s Store) bool {
	return checkRunning(ctx, profile, s) == nil
}

func checkRunning(ctx context.Context, profile string, s Store) error {
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
	bound, _ := BoundInterface(path)
	if bound == name && Running(ctx, profile, s) {
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
	if exec.CommandContext(ctx, "launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), servicePath(profile)).Run() != nil {
		return errors.New("无法启动 Cage 自带代理核心")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
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
