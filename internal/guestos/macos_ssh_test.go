package guestos

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func writeMacOSSSHConfig(t *testing.T, profile string, port int) {
	t.Helper()
	dir := filepath.Join(profile, "state", "lima", "mac")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := "Host lima-mac\n User cageprovision\n Port " + strconv.Itoa(port) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh.config"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMacOSScreenSharingSSHArgs(t *testing.T) {
	profile := t.TempDir()
	writeMacOSSSHConfig(t, profile, 55413)

	got, err := MacOSScreenSharingSSHArgs(profile, "blue", 5901)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"55413", "blue", "Hostname=127.0.0.1", "HostKeyAlias=cage-macos",
		"StrictHostKeyChecking=yes", "-N", "ExitOnForwardFailure=yes",
		"127.0.0.1:5901:127.0.0.1:5900",
	} {
		if !slices.Contains(got, value) {
			t.Errorf("missing %q in %q", value, got)
		}
	}
	if slices.Contains(got, "ClearAllForwardings=yes") {
		t.Fatal("screen sharing SSH must not clear local forwards")
	}
	if slices.Contains(got, "cageprovision") {
		t.Fatal("forward must use the ordinary guest account, not cageprovision")
	}

	// The Lima SSH port is reread each call because it changes between boots.
	writeMacOSSSHConfig(t, profile, 58716)
	got, err = MacOSScreenSharingSSHArgs(profile, "blue", 5910)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "58716") || slices.Contains(got, "55413") {
		t.Fatalf("did not reread the current SSH port: %q", got)
	}
	if !slices.Contains(got, "127.0.0.1:5910:127.0.0.1:5900") {
		t.Fatalf("unexpected forward: %q", got)
	}

	if _, err := MacOSScreenSharingSSHArgs(profile, "blue", 0); err == nil {
		t.Fatal("accepted an invalid host port")
	}

	command, err := MacOSSSHArgs(profile, "blue")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(command, "ClearAllForwardings=yes") {
		t.Fatal("regular SSH args must still clear forwards")
	}
	if slices.Contains(command, "-N") || slices.Contains(command, "ExitOnForwardFailure=yes") {
		t.Fatal("regular SSH args gained screen-sharing options")
	}
}
