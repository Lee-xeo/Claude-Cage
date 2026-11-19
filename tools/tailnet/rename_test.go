package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The node's folder from earlier versions is copied to its new name once,
// checked, and kept; later starts use the copy.
func TestStateFolderIsCopiedOnceAndKept(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	former := filepath.Join(home, ".config", "cage-tailnet")
	files := map[string]string{"tailscaled.state": "identity", "settings.json": `{"enabled":true}`, filepath.Join("certs", "host.key"): "ssh-key"}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(former, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(former, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(home, ".config", "tailnet")
	if dir := dataDir(); dir != current {
		t.Fatalf("folder %s, want %s", dir, current)
	}
	for name, content := range files {
		copied, err := os.ReadFile(filepath.Join(current, name))
		if err != nil || string(copied) != content {
			t.Fatalf("%s: %q %v", name, copied, err)
		}
		if kept, err := os.ReadFile(filepath.Join(former, name)); err != nil || string(kept) != content {
			t.Fatalf("the earlier folder lost %s: %v", name, err)
		}
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(current); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("folder mode %v %v", info.Mode(), err)
		}
	}
	// Changed later, the copy is the one in use; the earlier folder is not
	// read again.
	if err := os.WriteFile(filepath.Join(current, "settings.json"), []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if dir := dataDir(); dir != current {
		t.Fatalf("second start used %s", dir)
	}
	if data, _ := os.ReadFile(filepath.Join(current, "settings.json")); string(data) != `{"enabled":false}` {
		t.Fatalf("the copy was replaced: %s", data)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(home, ".config", "tailnet.copy-*")); len(leftovers) != 0 {
		t.Fatalf("staging left behind: %v", leftovers)
	}
}

// A folder that cannot be copied faithfully stays in use as it is.
func TestStateFolderThatCannotBeCopiedStaysInUse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating links needs privileges on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	former := filepath.Join(home, ".config", "cage-tailnet")
	if err := os.MkdirAll(former, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(former, "tailscaled.state"), []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(former, "odd")); err != nil {
		t.Fatal(err)
	}
	if dir := dataDir(); dir != former {
		t.Fatalf("folder %s, want the earlier one", dir)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "tailnet")); !os.IsNotExist(err) {
		t.Fatalf("a partial copy took the new name: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(home, ".config", "tailnet.copy-*")); len(leftovers) != 0 {
		t.Fatalf("staging left behind: %v", leftovers)
	}
}

func TestDeviceNameLeavesOutHowTheSystemWasSetUp(t *testing.T) {
	for hostname, want := range map[string]string{
		"lima-cage-qa-workspace-c9bcf9ea": "qa-workspace-c9bcf9ea",
		"cage-studio":                     "studio",
		"Workstation":                     "workstation",
		"lima-cage":                       "debian",
	} {
		if got := deviceNameFrom(hostname); got != want {
			t.Errorf("%s: %s, want %s", hostname, got, want)
		}
	}
}

// The SSH settings include the node's folder by its new name, and the
// include of its earlier name goes; everything else stays.
func TestSSHSettingsFollowTheFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	formerInclude := "Include \"" + filepath.ToSlash(filepath.Join(home, ".config", "cage-tailnet", "ssh_config")) + "\"\n"
	mine := "Host example\n  User me\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(formerInclude+mine), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &node{dir: filepath.Join(home, ".config", "tailnet")}
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := n.writeSSHConfig(nil); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(filepath.Join(sshDir, "config"))
	want := "Include \"" + filepath.ToSlash(filepath.Join(n.dir, "ssh_config")) + "\"\n" + mine
	if string(config) != want {
		t.Fatalf("SSH settings:\n%s\nwant:\n%s", config, want)
	}
	if strings.Contains(string(config), "cage-tailnet") {
		t.Fatal("the include of the earlier folder stayed")
	}
}
