//go:build windows

package hostbridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func shellCommand(command string) *exec.Cmd {
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

// userEnv is the environment for programs run on the user's behalf, without
// the application's own variables. Windows gives a desktop application the
// same PATH as a terminal.
func userEnv() []string { return baseEnv() }

// lookProgram leaves name for exec to find: programs get this process's own
// PATH here, so the two agree.
func lookProgram(name string, _ []string) string { return name }

var verifyWindowsPython = runWindowsPythonProbe

// resolveHostCommand resolves bare host plugin executables on Windows.
// Official Windows Python installers ship python.exe and py.exe without
// python3.exe, leaving "python3" to hit Microsoft Store's WindowsApps alias
// stub (which exits immediately with code 9009 / EOF on stdio MCP servers).
// Prefer real Python 3 interpreters on PATH or via `py -3`, and only accept a
// WindowsApps entry if it actually executes as Python.
func resolveHostCommand(ctx context.Context, name string, env []string) (string, []string, error) {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "python3", "python3.exe":
		return resolveWindowsPython(ctx, env, true)
	case "python", "python.exe":
		return resolveWindowsPython(ctx, env, false)
	default:
		return lookProgram(name, env), nil, nil
	}
}

func resolveWindowsPython(ctx context.Context, env []string, requirePython3 bool) (string, []string, error) {
	dirs := windowsPathDirs(env)
	type candidate struct {
		path   string
		prefix []string
	}
	var candidates []candidate
	seen := map[string]bool{}
	add := func(p string, prefix ...string) {
		key := strings.ToLower(filepath.Clean(p)) + "\x00" + strings.Join(prefix, "\x00")
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, candidate{path: p, prefix: prefix})
	}

	primaryNames := []string{"python3.exe", "python.exe"}
	if !requirePython3 {
		primaryNames = []string{"python.exe", "python3.exe"}
	}
	// 1. Prefer real interpreters outside Microsoft\WindowsApps.
	for _, filename := range primaryNames {
		for _, p := range findWindowsPathCandidates(dirs, filename, false) {
			add(p)
		}
	}
	// 2. Fall back to the Python Launcher (`py -3`).
	for _, p := range findWindowsPathCandidates(dirs, "py.exe", false) {
		add(p, "-3")
	}
	if windir := envValueFold(env, "WINDIR"); windir != "" {
		py := filepath.Join(windir, "py.exe")
		if windowsExecutableExists(py) {
			add(py, "-3")
		}
	}
	// 3. Accept a WindowsApps alias only when it genuinely runs Python (e.g.
	//    Python installed from Microsoft Store), never the Store redirect stub.
	for _, filename := range primaryNames {
		for _, p := range findWindowsPathCandidates(dirs, filename, true) {
			add(p)
		}
	}

	for _, cand := range candidates {
		if verifyWindowsPython(ctx, cand.path, cand.prefix, env, requirePython3) {
			return cand.path, cand.prefix, nil
		}
	}
	return "", nil, exec.ErrNotFound
}

func envValueFold(env []string, key string) string {
	val := ""
	prefix := key + "="
	for _, entry := range env {
		if len(entry) >= len(prefix) && strings.EqualFold(entry[:len(prefix)], prefix) {
			val = entry[len(prefix):]
		}
	}
	if val == "" {
		val = os.Getenv(key)
	}
	return val
}

func windowsPathDirs(env []string) []string {
	raw := envValueFold(env, "PATH")
	var dirs []string
	for _, part := range filepath.SplitList(raw) {
		part = strings.TrimSpace(strings.Trim(part, `"`))
		if part == "" || !filepath.IsAbs(part) {
			continue
		}
		dirs = append(dirs, part)
	}
	return dirs
}

func isWindowsAppsDir(dir string) bool {
	cleaned := strings.ToLower(filepath.Clean(dir))
	return strings.HasSuffix(cleaned, `\microsoft\windowsapps`) || strings.Contains(cleaned, `\microsoft\windowsapps\`)
}

func windowsExecutableExists(path string) bool {
	info, err := os.Stat(path)
	if err == nil {
		return !info.IsDir()
	}
	// App execution aliases in WindowsApps are reparse points whose Stat can
	// fail on some configurations while Lstat succeeds.
	linfo, lerr := os.Lstat(path)
	return lerr == nil && !linfo.IsDir()
}

func findWindowsPathCandidates(dirs []string, filename string, wantWindowsApps bool) []string {
	var found []string
	for _, dir := range dirs {
		if isWindowsAppsDir(dir) != wantWindowsApps {
			continue
		}
		candidate := filepath.Join(dir, filename)
		if windowsExecutableExists(candidate) {
			found = append(found, candidate)
		}
	}
	return found
}

func runWindowsPythonProbe(ctx context.Context, program string, prefix []string, env []string, requirePython3 bool) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	snippet := "import sys; raise SystemExit(0 if sys.version_info[0] == 3 else 1)"
	if !requirePython3 {
		snippet = "import sys; raise SystemExit(0)"
	}
	args := append(append([]string(nil), prefix...), "-c", snippet)
	cmd := exec.CommandContext(probeCtx, program, args...)
	cmd.Env = env
	hideWindow(cmd)
	return cmd.Run() == nil
}

// hideWindow keeps a console program from opening a window of its own: the
// application has no console for it to share.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if kill.Run() != nil {
		_ = cmd.Process.Kill()
	}
}
