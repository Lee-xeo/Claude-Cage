package workbench

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDetachedServiceLeavesCallerJobWhenPermitted(t *testing.T) {
	inside := detachedProcessAttributes(false)
	outside := detachedProcessAttributes(true)
	for _, attributes := range []*syscall.SysProcAttr{inside, outside} {
		if !attributes.HideWindow || attributes.CreationFlags&windows.CREATE_NO_WINDOW == 0 || attributes.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
			t.Fatalf("background service must stay windowless in its own process group: %#v", attributes)
		}
	}
	if inside.CreationFlags&windows.CREATE_BREAKAWAY_FROM_JOB != 0 || outside.CreationFlags&windows.CREATE_BREAKAWAY_FROM_JOB == 0 {
		t.Fatalf("breakaway flags: inside %#x, outside %#x", inside.CreationFlags, outside.CreationFlags)
	}
	// exec.Cmd.Start wraps CreateProcess failures in *os.PathError.
	if !breakawayRefused(&os.PathError{Op: "fork/exec", Path: "cage.exe", Err: windows.ERROR_ACCESS_DENIED}) {
		t.Fatal("a job that forbids breakaway must fall back to starting inside it")
	}
	if breakawayRefused(&os.PathError{Op: "fork/exec", Path: "cage.exe", Err: windows.ERROR_FILE_NOT_FOUND}) || breakawayRefused(errors.New("other")) {
		t.Fatal("unrelated start failures must not be retried")
	}
}

func TestStartDetachedRunsService(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var started *exec.Cmd
	if err := startDetached(func() *exec.Cmd {
		started = exec.Command(executable, "-test.run=^$")
		return started
	}); err != nil {
		t.Fatal(err)
	}
	if started.Process == nil {
		t.Fatal("service was not started")
	}
}
