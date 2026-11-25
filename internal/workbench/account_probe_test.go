package workbench

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFakeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

func runOfflineNamespaceWrapper(t *testing.T, binDir, payload string) (stdout, stderr string, err error) {
	t.Helper()
	script := wrapOfflineAccountNamespace(payload)
	cmd := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", script)
	home := t.TempDir()
	cmd.Env = []string{
		"PATH=" + binDir + ":/usr/bin:/bin",
		"HOME=" + home,
		"USER=cage",
		"LOGNAME=cage",
		"TERM=xterm-256color",
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func TestWrapOfflineAccountNamespacePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell namespace wrapper is executed inside the Linux guest")
	}

	t.Run("privileged sudo unshare -nm drops privileges and runs payload once", func(t *testing.T) {
		dir := t.TempDir()
		trace := filepath.Join(dir, "trace.log")
		runs := filepath.Join(dir, "runs.log")

		writeFakeTool(t, dir, "id", `echo 1000`)
		writeFakeTool(t, dir, "sudo", `
echo "sudo $*" >> "`+trace+`"
export SUDO_UID=1000 SUDO_GID=1000 SUDO_USER=cage SUDO_COMMAND=probe
while [ $# -gt 0 ]; do
  case "$1" in
    -n|--preserve-env=*) shift ;;
    *) break ;;
  esac
done
exec "$@"`)
		writeFakeTool(t, dir, "unshare", `
echo "unshare $*" >> "`+trace+`"
if [ "$1" != "-nm" ]; then exit 99; fi
shift
exec "$@"`)
		writeFakeTool(t, dir, "mount", `echo "mount $*" >> "`+trace+`"`)
		writeFakeTool(t, dir, "setpriv", `
echo "setpriv $*" >> "`+trace+`"
while [ $# -gt 0 ]; do
  case "$1" in
    --reuid=*|--regid=*|--init-groups) shift ;;
    *) break ;;
  esac
done
exec "$@"`)

		payload := `echo run >> "` + runs + `"; printf 'ok route=%s sudo=%s home=%s\n' "${ROUTE_COMMAND:-}" "${SUDO_UID:-}" "${HOME:+set}"`
		stdout, stderr, err := runOfflineNamespaceWrapper(t, dir, payload)
		if err != nil {
			t.Fatalf("wrapper failed: %v (stderr=%q)", err, stderr)
		}
		if strings.TrimSpace(stdout) != "ok route= sudo= home=set" {
			t.Fatalf("unexpected stdout: %q", stdout)
		}
		runData, _ := os.ReadFile(runs)
		if lines := strings.Fields(string(runData)); len(lines) != 1 {
			t.Fatalf("payload ran %d times, want 1", len(lines))
		}
		traceData, _ := os.ReadFile(trace)
		traceText := string(traceData)
		if !strings.Contains(traceText, "sudo -n --preserve-env=ROUTE_COMMAND,HOME,USER,LOGNAME,TERM unshare -nm") {
			t.Fatalf("expected sudo -n unshare -nm in trace:\n%s", traceText)
		}
		if !strings.Contains(traceText, "setpriv --reuid=1000 --regid=1000 --init-groups") {
			t.Fatalf("expected setpriv privilege drop in trace:\n%s", traceText)
		}
	})

	t.Run("non-root EPERM falls back to unshare -Urnm without setpriv", func(t *testing.T) {
		dir := t.TempDir()
		trace := filepath.Join(dir, "trace.log")
		runs := filepath.Join(dir, "runs.log")

		writeFakeTool(t, dir, "id", `echo 1000`)
		writeFakeTool(t, dir, "sudo", `
echo "sudo: a password is required" >&2
exit 1`)
		writeFakeTool(t, dir, "unshare", `
echo "unshare $*" >> "`+trace+`"
if [ "$1" = "-nm" ]; then
  echo "unshare: unshare failed: Operation not permitted" >&2
  exit 1
fi
if [ "$1" = "-Urnm" ]; then
  shift
  exec "$@"
fi
exit 99`)
		writeFakeTool(t, dir, "mount", `echo "mount $*" >> "`+trace+`"`)
		writeFakeTool(t, dir, "setpriv", `
echo "setpriv should not be called in unprivileged userns" >&2
exit 98`)

		payload := `echo run >> "` + runs + `"; printf 'fallback route=%s home=%s\n' "${ROUTE_COMMAND:-}" "${HOME:+set}"`
		stdout, stderr, err := runOfflineNamespaceWrapper(t, dir, payload)
		if err != nil {
			t.Fatalf("fallback failed: %v (stderr=%q)", err, stderr)
		}
		if strings.TrimSpace(stdout) != "fallback route= home=set" {
			t.Fatalf("unexpected stdout: %q", stdout)
		}
		runData, _ := os.ReadFile(runs)
		if lines := strings.Fields(string(runData)); len(lines) != 1 {
			t.Fatalf("payload ran %d times, want 1", len(lines))
		}
		traceData, _ := os.ReadFile(trace)
		traceText := string(traceData)
		if !strings.Contains(traceText, "unshare -Urnm") {
			t.Fatalf("expected unshare -Urnm in trace:\n%s", traceText)
		}
		if !strings.Contains(traceText, "mount --make-rprivate /") {
			t.Fatalf("expected mount --make-rprivate / in trace:\n%s", traceText)
		}
	})

	t.Run("rewritten unshare shim failing setpriv falls back cleanly to unshare -Urnm", func(t *testing.T) {
		dir := t.TempDir()
		runs := filepath.Join(dir, "runs.log")

		writeFakeTool(t, dir, "id", `echo 1000`)
		writeFakeTool(t, dir, "sudo", `
export SUDO_UID=1000 SUDO_GID=1000
while [ $# -gt 0 ]; do
  case "$1" in
    -n|--preserve-env=*) shift ;;
    *) break ;;
  esac
done
exec "$@"`)
		// Simulates a user's /usr/local/bin/unshare shim that rewrites -nm to -Urnm
		// under sudo, causing setpriv --reuid=1000 to fail with EINVAL.
		writeFakeTool(t, dir, "unshare", `
if [ "$1" = "-nm" ]; then
  export IN_SINGLE_UID_USERNS=1
  shift
  exec "$@"
fi
if [ "$1" = "-Urnm" ]; then
  unset IN_SINGLE_UID_USERNS
  shift
  exec "$@"
fi
exit 99`)
		writeFakeTool(t, dir, "mount", `exit 0`)
		writeFakeTool(t, dir, "setpriv", `
if [ "${IN_SINGLE_UID_USERNS:-}" = "1" ]; then
  echo "setpriv: setresuid failed: Invalid argument" >&2
  exit 1
fi
exit 98`)

		payload := `echo run >> "` + runs + `"; echo shim-ok`
		stdout, stderr, err := runOfflineNamespaceWrapper(t, dir, payload)
		if err != nil {
			t.Fatalf("shim fallback failed: %v (stderr=%q)", err, stderr)
		}
		if strings.TrimSpace(stdout) != "shim-ok" {
			t.Fatalf("unexpected stdout: %q", stdout)
		}
		runData, _ := os.ReadFile(runs)
		if lines := strings.Fields(string(runData)); len(lines) != 1 {
			t.Fatalf("payload ran %d times, want 1", len(lines))
		}
	})

	t.Run("root guest uses unshare -nm directly without sudo or setpriv", func(t *testing.T) {
		dir := t.TempDir()
		trace := filepath.Join(dir, "trace.log")
		runs := filepath.Join(dir, "runs.log")

		writeFakeTool(t, dir, "id", `echo 0`)
		writeFakeTool(t, dir, "sudo", `echo "sudo should not be called as root" >&2; exit 97`)
		writeFakeTool(t, dir, "setpriv", `echo "setpriv should not be called as root" >&2; exit 98`)
		writeFakeTool(t, dir, "unshare", `
echo "unshare $*" >> "`+trace+`"
if [ "$1" != "-nm" ]; then exit 99; fi
shift
exec "$@"`)
		writeFakeTool(t, dir, "mount", `echo "mount $*" >> "`+trace+`"`)

		payload := `echo run >> "` + runs + `"; echo root-ok`
		stdout, stderr, err := runOfflineNamespaceWrapper(t, dir, payload)
		if err != nil {
			t.Fatalf("root path failed: %v (stderr=%q)", err, stderr)
		}
		if strings.TrimSpace(stdout) != "root-ok" {
			t.Fatalf("unexpected stdout: %q", stdout)
		}
		runData, _ := os.ReadFile(runs)
		if lines := strings.Fields(string(runData)); len(lines) != 1 {
			t.Fatalf("payload ran %d times, want 1", len(lines))
		}
	})

	t.Run("fails closed when neither unshare -nm nor unshare -Urnm is permitted", func(t *testing.T) {
		dir := t.TempDir()
		runs := filepath.Join(dir, "runs.log")

		writeFakeTool(t, dir, "id", `echo 1000`)
		writeFakeTool(t, dir, "sudo", `echo "sudo: a password is required" >&2; exit 1`)
		writeFakeTool(t, dir, "unshare", `echo "unshare: unshare failed: Operation not permitted" >&2; exit 1`)
		writeFakeTool(t, dir, "mount", `exit 0`)

		payload := `echo leaked >> "` + runs + `"`
		_, stderr, err := runOfflineNamespaceWrapper(t, dir, payload)
		if err == nil {
			t.Fatal("expected wrapper to fail closed when all namespace options fail")
		}
		if _, statErr := os.Stat(runs); !os.IsNotExist(statErr) {
			t.Fatal("payload executed without namespace isolation")
		}
		classified := classifyAccountProbeError(err, stderr)
		if !strings.Contains(classified.Error(), "命名空间或 sudo 权限受限") || !strings.Contains(classified.Error(), "退出码 1") {
			t.Fatalf("unexpected classification: %q (stderr=%q)", classified.Error(), stderr)
		}
		if !errors.Is(classified, err) {
			t.Fatal("classified error lost underlying error chain")
		}
	})
}

func TestClassifyAccountProbeError(t *testing.T) {
	runExit := func(code string) error {
		if runtime.GOOS == "windows" {
			return exec.Command("cmd", "/C", "exit", code).Run()
		}
		return exec.Command("/bin/sh", "-c", "exit "+code).Run()
	}
	exit1 := runExit("1")
	exit127 := runExit("127")
	exit255 := runExit("255")

	cases := []struct {
		name   string
		err    error
		stderr string
		want   string
	}{
		{
			name:   "unshare EPERM",
			err:    exit1,
			stderr: "unshare: unshare failed: Operation not permitted\n",
			want:   "命名空间或 sudo 权限受限",
		},
		{
			name:   "sudo password required",
			err:    exit1,
			stderr: "sudo: a password is required\n",
			want:   "命名空间或 sudo 权限受限",
		},
		{
			name:   "setpriv EINVAL",
			err:    exit1,
			stderr: "setpriv: setresuid failed: Invalid argument\n",
			want:   "命名空间或 sudo 权限受限",
		},
		{
			name:   "claude CLI missing",
			err:    exit1,
			stderr: "未找到官方 Claude Code CLI，请先在环境中安装后重试。\n",
			want:   "未找到官方 Claude Code CLI",
		},
		{
			name:   "subscription status invalid",
			err:    exit1,
			stderr: "无法读取 Claude 订阅状态，请确认官方 CLI 可正常运行后重试；现有登录信息已保留。\n",
			want:   "无法读取 Claude 订阅状态",
		},
		{
			name:   "deadline exceeded",
			err:    context.DeadlineExceeded,
			stderr: "",
			want:   "读取账号状态超时",
		},
		{
			name:   "guest helper missing",
			err:    exit127,
			stderr: "/bin/bash: line 1: /usr/local/bin/cage-claude-account: No such file or directory\n",
			want:   "客机账号组件缺失或不可执行",
		},
		{
			name:   "ssh unreachable",
			err:    exit255,
			stderr: "ssh: connect to host 127.0.0.1 port 60022: Connection refused\n",
			want:   "无法连接客机以读取账号状态",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAccountProbeError(tc.err, tc.stderr)
			if !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("got %q, want substring %q", got.Error(), tc.want)
			}
			if !errors.Is(got, tc.err) {
				t.Fatalf("classified error %v does not unwrap to %v", got, tc.err)
			}
		})
	}
}

func TestAccountProbeErrorsPreserveClassifiedReason(t *testing.T) {
	s := testServer(t)
	s.ready = true
	cause := errors.New("exit status 1")
	nsErr := &accountProbeError{
		message:  "无法隔离读取账号状态：客机命名空间或 sudo 权限受限，请检查客机 unshare/user namespace 权限（退出码 1）",
		cause:    cause,
		exitCode: 1,
	}
	s.claudeAccounts.probe = func(context.Context, string) (claudeGuestSnapshot, error) {
		return claudeGuestSnapshot{}, nsErr
	}

	call := s.claudeAccounts.claim("0123456789abcdef01234567")
	s.runClaudeRefresh(context.Background(), "0123456789abcdef01234567", "0123456789abcdef01234567", call, false)
	if !strings.Contains(call.err.Error(), "命名空间或 sudo 权限受限") {
		t.Fatalf("runClaudeRefresh hid classified probe error: %v", call.err)
	}
	if !errors.Is(call.err, cause) {
		t.Fatalf("runClaudeRefresh lost cause chain: %v", call.err)
	}

	_, _, err := s.inspectClaudeDefault(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "命名空间或 sudo 权限受限") {
		t.Fatalf("inspectClaudeDefault hid classified probe error: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("inspectClaudeDefault lost cause chain: %v", err)
	}
}
