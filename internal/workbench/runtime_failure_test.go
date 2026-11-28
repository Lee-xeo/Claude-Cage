package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestFirstStartReportsUnreachableProxyBeforeImageSetup(t *testing.T) {
	// The startup path validates Lima's socket length before it reaches the
	// proxy. Keep the isolated profile short, including on Windows CI where
	// TMP normally points inside a per-run directory under RUNNER_TEMP.
	switch runtime.GOOS {
	case "darwin", "linux":
		t.Setenv("TMPDIR", "/tmp")
	case "windows":
		root := os.Getenv("RUNNER_TEMP")
		if root == "" {
			var err error
			root, err = os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("TMP", root)
		t.Setenv("TEMP", root)
	}
	s := testServer(t)
	if err := writePrivate(filepath.Join(s.profile, "environment.json"), cage.EnvironmentRecord{Directory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	setupUnreachableManagedProxy(t, s)
	physical, validate, start := physicalProxyInterface, validateProxyCore, startProxyCore
	t.Cleanup(func() { physicalProxyInterface, validateProxyCore, startProxyCore = physical, validate, start })
	physicalProxyInterface = func(context.Context) (string, error) { return "test-uplink", nil }
	validateProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	startProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	if _, err := s.submitJob("start", func() error { return s.startEnvironment(false) }); err != nil {
		t.Fatal(err)
	}
	s.jobs.workers.Wait()
	job := s.jobs.snapshot()
	if job.SetupProgress != nil || !strings.Contains(job.Error, "代理出口预检未通过") || !strings.Contains(job.Error, "首跳状态未确定") || !strings.Contains(job.Error, "代理连接失败") {
		t.Fatalf("expected a proxy preflight failure before image preparation, got %+v", job)
	}
}

func TestStartEnvironmentRejectsLimaSocketPathBeforeProxy(t *testing.T) {
	profile := filepath.Join(t.TempDir(), strings.Repeat("private-path-", 9))
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	s := &Server{profile: profile, configPath: filepath.Join(profile, "config.json")}
	data, err := json.Marshal(cage.EnvironmentRecord{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "environment.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	// No configuration or proxy exists yet. The path diagnostic should come
	// first and never include the private directory.
	err = s.startEnvironment(false)
	if !errors.Is(err, cage.ErrLimaSocketPathTooLong) || !strings.Contains(err.Error(), "更短的本地目录") ||
		strings.Contains(err.Error(), profile) {
		t.Fatalf("start did not report a path-free Lima preflight failure: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profile, "container.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.startEnvironment(false); err == nil || errors.Is(err, cage.ErrLimaSocketPathTooLong) {
		t.Fatalf("container should report its own invalid config without checking Lima: %v", err)
	}
}

func TestRunCagePreservesOriginalVMError(t *testing.T) {
	if os.Getenv("CAGE_TEST_START_FAILURE_HELPER") == "1" {
		fmt.Fprintln(os.Stderr, "Error: QEMU 启动失败：WHPX: Failed to enable nested virtualization, hr=80370302 token=private-test-token")
		fmt.Fprintln(os.Stderr, "Error: Failed to start base VM. Full log: local diagnostic logs")
		os.Exit(7)
	}
	t.Setenv("CAGE_TEST_START_FAILURE_HELPER", "1")
	s := testServer(t)
	if err := writePrivate(s.profile+string(os.PathSeparator)+"environment.json", map[string]string{"directory": t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.exe = exe
	if _, err := s.submitJob("setup", func() error { return s.runCage("-test.run=^TestRunCagePreservesOriginalVMError$") }); err != nil {
		t.Fatal(err)
	}
	job := awaitJobIdle(t, &s.jobs)
	if !strings.Contains(job.Error, "80370302") || strings.Contains(job.Error, "private-test-token") || !strings.Contains(job.Log, "Failed to start base VM") {
		t.Fatalf("lost or exposed the original error: %+v", job)
	}
}

func TestRunCageExplainsCommonFailure(t *testing.T) {
	if output := os.Getenv("CAGE_TEST_READABLE_FAILURE"); output != "" {
		fmt.Fprintln(os.Stderr, output)
		os.Exit(7)
	}
	for _, tc := range []struct{ output, want string }{
		{"Error: QEMU 启动失败：cannot set up guest memory 'pc.ram': Cannot allocate memory\nError: Failed to start base VM", "虚拟机内存分配失败。请关闭占用内存的应用，或在“计算资源”中调低内存上限后重试"},
		{"qemu[stderr]: cannot set up guest memory 'pc.ram'\nDriver stopped due to error: exit status 1", "虚拟机内存分配失败"},
		{"Error: " + cage.WindowsStartupMemoryLow, "释放内存后重试"},
		{"curl: (28) Operation timed out", "超时"},
		{"E: No space left on device", "空间不足"},
		{"curl: (60) certificate verify failed", "证书验证"},
		{"Error: Base VM not found. Run 'agent-vm setup' first.", "基础模板缺失"},
		{"Error: Base VM setup did not complete. Run 'agent-vm setup' again.", "基础模板尚未安装完成"},
		{"Error: could not query Lima: agent-vm's Lima home /Users/alice/.claude-cage/state/lima is also your own Lima's home: set AGENT_VM_LIMA_HOME to a folder for agent-vm alone", "LIMA_HOME"},
		{"unrecognized failure token=private-error-secret", "退出码 7"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Setenv("CAGE_TEST_READABLE_FAILURE", tc.output)
			s := testServer(t)
			if err := writePrivate(s.profile+string(os.PathSeparator)+"environment.json", map[string]string{"directory": t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			s.exe = exe
			if _, err := s.submitJob("setup", func() error { return s.runCage("-test.run=^TestRunCageExplainsCommonFailure$") }); err != nil {
				t.Fatal(err)
			}
			job := awaitJobIdle(t, &s.jobs)
			if !strings.Contains(job.Error, tc.want) || strings.Contains(job.Error, "private-error-secret") || !strings.Contains(job.Message, "准备环境") {
				t.Fatalf("missing readable operation failure: %+v", job)
			}
		})
	}
}
