//go:build !windows

package workbench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInternalEnvironmentStopKeepsProxy(t *testing.T) {
	s := transferMountServer(t, false, "current")
	bin := t.TempDir()
	args := filepath.Join(bin, "stop-args")
	t.Setenv("CAGE_TEST_STOP_ARGS", args)
	s.exe = filepath.Join(bin, "cage")
	if err := os.WriteFile(s.exe, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAGE_TEST_STOP_ARGS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.stopEnvironment(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(args)
	if err != nil || string(got) != "stop\n--keep-proxy\n" {
		t.Fatalf("internal environment stop must preserve the active proxy: %q, %v", got, err)
	}
}
