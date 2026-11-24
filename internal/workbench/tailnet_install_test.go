package workbench

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Set in the stand-in process so the helper below only sleeps, playing the
// still-running earlier program.
const tailnetSleepHelperEnv = "GO_WANT_TAILNET_SLEEP_HELPER"

// TestTailnetInstallSleepHelper is not a test on its own: in a copy of the test
// binary started with the variable above, it sleeps while the copy is replaced.
func TestTailnetInstallSleepHelper(t *testing.T) {
	if os.Getenv(tailnetSleepHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

func TestTailnetInstallAtomicallyReplacesRunningPayloadAndPreservesIdentity(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("guest installation executes on Unix")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	sudoers := filepath.Join(root, "sudoers")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sudoers, 0700); err != nil {
		t.Fatal(err)
	}
	// Copy this test binary as the stand-in, not the system sleep: a copied
	// system program can hang on macOS arm64 and block Wait.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	// An installation from an earlier version, under the program's old name.
	former := filepath.Join(bin, "cage-tailnet")
	if err = os.WriteFile(former, old, 0755); err != nil {
		t.Fatal(err)
	}
	formerRule := filepath.Join(sudoers, "cage-tailnet")
	if err = os.WriteFile(formerRule, []byte("old rule\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "tailnet")
	identity := filepath.Join(root, "identity.json")
	if err = os.WriteFile(identity, []byte("saved-node-and-keys"), 0600); err != nil {
		t.Fatal(err)
	}
	// This is a real open executable inode, rather than a mocked install result.
	running := exec.Command(former, "-test.run=^TestTailnetInstallSleepHelper$")
	running.Env = append(os.Environ(), tailnetSleepHelperEnv+"=1")
	if err = running.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = running.Process.Kill()
		waited := make(chan struct{})
		go func() { _ = running.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
			t.Error("the replaced process did not exit after being killed")
		}
	}()
	if err = os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(tailnetInstallScript, "/usr/local/bin", bin)
	script = strings.ReplaceAll(script, "/etc/sudoers.d", sudoers)
	fresh := []byte("#!/bin/sh\nprintf updated-tailnet\n")
	install := exec.Command("/bin/bash", "-c", script)
	install.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	install.Stdin = bytes.NewReader(fresh)
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("running binary replacement failed: %v %s", err, output)
	}
	output, err := exec.Command(executable).Output()
	if err != nil || string(output) != "updated-tailnet" {
		t.Fatalf("new process uses old payload: %q %v", output, err)
	}
	if data, err := os.ReadFile(identity); err != nil || string(data) != "saved-node-and-keys" {
		t.Fatal("installation touched identity", err)
	}
	if _, err := os.Stat(former); !os.IsNotExist(err) {
		t.Fatal("the program under its old name was left behind", err)
	}
	if _, err := os.Stat(formerRule); !os.IsNotExist(err) {
		t.Fatal("the old sudo rule was left behind", err)
	}
	if rule, err := os.ReadFile(filepath.Join(sudoers, "tailnet")); err != nil || !strings.Contains(string(rule), executable) {
		t.Fatalf("sudo rule for the program: %q %v", rule, err)
	}
	if entries, err := filepath.Glob(filepath.Join(bin, ".tailnet.*")); err != nil || len(entries) != 0 {
		t.Fatal("staged payload left behind", entries, err)
	}
	// A failed staged copy must leave the working binary and identity in place.
	if err = os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nif test \"$1\" = install; then exit 42; fi\nexec \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	failed := exec.Command("/bin/bash", "-c", script)
	failed.Env = install.Env
	failed.Stdin = strings.NewReader("broken candidate")
	if output, err := failed.CombinedOutput(); err == nil {
		t.Fatalf("failed staged copy reported success: %s", output)
	}
	if actual, err := os.ReadFile(executable); err != nil || !bytes.Equal(actual, fresh) {
		t.Fatal("failed installation replaced the working binary", err)
	}
	if actual, err := os.ReadFile(identity); err != nil || string(actual) != "saved-node-and-keys" {
		t.Fatal("failed installation touched identity", err)
	}
	if entries, err := filepath.Glob(filepath.Join(bin, ".tailnet.*")); err != nil || len(entries) != 0 {
		t.Fatal("failed installation leaked its stage", entries, err)
	}
}
