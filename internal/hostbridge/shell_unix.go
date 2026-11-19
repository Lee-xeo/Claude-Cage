//go:build !windows

package hostbridge

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

func userShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// shellCommand runs the command in the user's own shell, so it sees the same
// PATH and tools as their terminal.
func shellCommand(command string) *exec.Cmd {
	cmd := exec.Command(userShell(), "-c", command)
	cmd.Env = userEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

const pathMark = "\x1ePATH\x1e"

// terminalPath is the PATH the user's terminal has. An application opened
// from the desktop is given only the system's own, without the directories
// the user's shell adds when it starts, where their tools usually are.
var terminalPath = sync.OnceValue(readTerminalPath)

func readTerminalPath() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.Command(userShell(), "-l", "-i", "-c", `printf '%s' "`+pathMark+`${PATH}`+pathMark+`"`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return ""
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		killTree(cmd)
		<-done
	}
	// The shell's own start-up files may print around the value.
	_, rest, found := strings.Cut(out.String(), pathMark)
	path, _, closed := strings.Cut(rest, pathMark)
	if !found || !closed || !strings.Contains(path, "/") {
		return ""
	}
	return path
}

// userEnv is this process's environment with the PATH of the user's
// terminal, for the programs run on their behalf.
func userEnv() []string {
	path := terminalPath()
	if path == "" {
		return os.Environ()
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			env = append(env, entry)
		}
	}
	return append(env, "PATH="+path)
}
