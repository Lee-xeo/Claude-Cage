package managedproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/host"
)

var (
	preflightPhysicalInterface = PhysicalInterface
	preflightCorePath          = CorePath
)

// PreflightProxy is a short-lived, isolated core instance used to probe
// candidate bootstrap/guest DNS settings without altering the active proxy.
type PreflightProxy struct {
	Endpoint string
	cleanup  func()
}

// Close stops the temporary core process and removes its temporary files.
func (p *PreflightProxy) Close() {
	if p != nil && p.cleanup != nil {
		p.cleanup()
	}
}

func preflightSocketPath(profile string) (sockPath, cleanupDir string, err error) {
	if runtime.GOOS == "windows" {
		// Use the same private ASCII directory and 11-byte filename budget as
		// WindowsSocket so AF_UNIX works outside AppData/Temp (#484) without
		// colliding with the active core's proxy.sock.
		dir, err := host.PrivateSocketDir(filepath.Clean(profile), len(`\proxy.sock`), true)
		if err != nil {
			return "", "", err
		}
		return filepath.Join(dir, "p"+Token()[:4]+".sock"), "", nil
	}
	dir, err := os.MkdirTemp("/tmp", fmt.Sprintf("cage-pf-%d-", os.Getuid()))
	if err != nil {
		return "", "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return filepath.Join(dir, "control.sock"), dir, nil
}

// StartPreflight starts an isolated Mihomo instance for s's route with
// candidate DNS resolvers, a fresh loopback port, and one-time credentials.
// The active profile's proxy configuration, control socket, and running core
// are never modified.
func StartPreflight(ctx context.Context, profile string, s Store) (*PreflightProxy, error) {
	if _, err := s.Route(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		return nil, errors.New("内置代理不支持当前系统")
	}
	iface, err := preflightPhysicalInterface(ctx)
	if err != nil {
		return nil, err
	}
	core, err := preflightCorePath()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("无法为候选代理探测分配本机端口：%w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	workDir, err := os.MkdirTemp("", "cage-proxy-preflight-*")
	if err != nil {
		return nil, err
	}
	cleanupWork := func() { _ = os.RemoveAll(workDir) }
	if err := host.EnsurePrivateDir(workDir); err != nil {
		cleanupWork()
		return nil, err
	}

	sockPath, sockCleanupDir, err := preflightSocketPath(profile)
	if err != nil {
		cleanupWork()
		return nil, err
	}
	cleanupFiles := func() {
		_ = os.Remove(sockPath)
		if sockCleanupDir != "" {
			_ = os.RemoveAll(sockCleanupDir)
		}
		cleanupWork()
	}

	candidate := s
	candidate.Port = port
	candidate.Auth = Token()
	raw, err := candidate.runtimeWithSocket(sockPath)
	if err != nil {
		cleanupFiles()
		return nil, err
	}
	raw, err = BindInterface(raw, iface)
	if err != nil {
		cleanupFiles()
		return nil, err
	}
	configPath := filepath.Join(workDir, "config.yaml")
	if err := Write(configPath, raw); err != nil {
		cleanupFiles()
		return nil, err
	}

	valCtx, valCancel := context.WithTimeout(ctx, 15*time.Second)
	valCmd := exec.CommandContext(valCtx, core, "-t", "-d", workDir, "-f", configPath)
	host.HideCommandWindow(valCmd)
	valCmd.Env = coreEnvironment(workDir)
	valOut := diagnostics.New(profile).Stream("proxy-validation", "error")
	valCmd.Stdout, valCmd.Stderr = valOut, valOut
	valErr := valCmd.Run()
	valCancel()
	_ = valOut.Close()
	if valErr != nil {
		cleanupFiles()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("随附的 Mihomo 核心未通过配置校验，请核对节点协议和参数；可在设置的“日志与诊断”中导出诊断包查看校验记录；原配置已保留")
	}

	_ = os.Remove(sockPath)
	logStream := diagnostics.New(profile).Stream("proxy-preflight", "warn")
	cmd := exec.Command(core, "-d", workDir, "-f", configPath)
	host.HideCommandWindow(cmd)
	cmd.Env = coreEnvironment(workDir)
	cmd.Stdout, cmd.Stderr = logStream, logStream
	if err := cmd.Start(); err != nil {
		_ = logStream.Close()
		cleanupFiles()
		return nil, err
	}

	done := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = cmd.Wait()
		_ = logStream.Close()
		close(done)
	}()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
			cleanupFiles()
		})
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(coreReadyTimeout)
	defer deadline.Stop()
	var lastErr error
	for {
		select {
		case <-done:
			cleanup()
			return nil, fmt.Errorf("候选代理核心启动失败：%w", &coreError{stage: "preflight.exit", err: exitErr})
		case <-ctx.Done():
			cleanup()
			return nil, ctx.Err()
		case <-deadline.C:
			cleanup()
			return nil, errors.Join(errors.New("代理核心启动超时，请查看私有代理日志"), lastErr)
		case <-ticker.C:
			lastErr = checkRunningSocket(ctx, sockPath, iface, candidate)
			if lastErr == nil {
				if err := host.ProtectFile(sockPath); err != nil {
					cleanup()
					return nil, &coreError{stage: "preflight.socket.protect", err: err}
				}
				return &PreflightProxy{Endpoint: candidate.Endpoint(), cleanup: cleanup}, nil
			}
		}
	}
}
