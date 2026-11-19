//go:build windows

package hostbridge

import (
	"os/exec"
	"strconv"
	"syscall"
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
