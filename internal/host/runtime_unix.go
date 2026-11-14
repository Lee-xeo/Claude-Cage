//go:build !windows

package host

import "os/exec"

func HideCommandWindow(cmd *exec.Cmd) {}

func windowsHypervisorReady() error { return nil }
