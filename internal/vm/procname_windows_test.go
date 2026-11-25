package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsQEMUIdentityHandlesSpacesAndRequiresInstance(t *testing.T) {
	dir := `C:\Users\测试 User\.claude-cage\state\lima\test`
	args := []string{`C:\Program Files\Cage\resources\runtime\qemu\qemu-system-x86_64.exe`, "-name", "lima-test", "-pidfile", dir + `\qemu.pid`}
	if !windowsQEMUCommandMatches(windows.ComposeCommandLine(args), dir, "test") {
		t.Fatal("quoted Unicode QEMU identity not recognized")
	}
	if windowsQEMUCommandMatches(windows.ComposeCommandLine(args), dir, "other") {
		t.Fatal("another instance was accepted")
	}
	args[4] = dir + `-other\qemu.pid`
	if windowsQEMUCommandMatches(windows.ComposeCommandLine(args), dir, "test") {
		t.Fatal("another instance PID file was accepted")
	}
}

func TestWindowsQEMUIdentityNativeCommandLine(t *testing.T) {
	cmd, ok := windowsProcessCommandLine(os.Getpid())
	if !ok || strings.TrimSpace(cmd) == "" {
		t.Fatalf("native NtQueryInformationProcess failed for current process: ok=%v cmd=%q", ok, cmd)
	}
	args, err := windows.DecomposeCommandLine(cmd)
	if err != nil || len(args) == 0 {
		t.Fatalf("failed to decompose native command line %q: %v", cmd, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Base(args[0]), filepath.Base(exe)) {
		t.Fatalf("command line executable %q did not match %q", args[0], exe)
	}
}
