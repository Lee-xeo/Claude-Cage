//go:build !windows

package workbench

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAccountDesktopResizeScriptReachesTheInstanceDisplay(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if runtime.GOOS != "linux" || err != nil {
		t.Skip("the script identifies its settings manager through /proc")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Dir(executable), "cage-instance-resize-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	id := strings.Repeat("e", 24)
	run, bin := filepath.Join(root, "run"), filepath.Join(root, "bin")
	for _, directory := range []string{run, bin} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		data, _ := os.ReadFile(path)
		return string(data)
	}
	t.Setenv("CAGE_RESIZE_FIXTURE", root)
	write(filepath.Join(bin, "systemctl"), "#!/bin/sh\ncat \"$CAGE_RESIZE_FIXTURE/main-pid\"\n", 0o700)
	// The stand-in enters nothing: it records the namespace request and runs the command.
	write(filepath.Join(bin, "sudo"), "#!/bin/sh\necho \"$*\" >> \"$CAGE_RESIZE_FIXTURE/sudo\"\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n", 0o700)
	write(filepath.Join(bin, "xrandr"), "#!/bin/sh\necho \"$DISPLAY $XAUTHORITY $*\" >> \"$CAGE_RESIZE_FIXTURE/modes\"\n", 0o700)
	write(filepath.Join(bin, "xsettingsd"), "#!/bin/sh\ntrap 'echo reload >> \"$CAGE_RESIZE_FIXTURE/reloads\"' HUP\nwhile :; do sleep 1 & wait $!; done\n", 0o700)
	write(filepath.Join(root, "main-pid"), "4242\n", 0o600)
	write(filepath.Join(run, "display"), "3\n", 0o600)
	resize := func(scale string) (string, error) {
		t.Helper()
		script := strings.ReplaceAll(accountDesktopResizeScript(id, 1000, 800, scale), "/run/desktop-"+id, run)
		cmd := exec.Command(bash, "--noprofile", "--norc", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	reloads := func(want int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for strings.Count(read(filepath.Join(root, "reloads")), "reload") < want && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
		if got := strings.Count(read(filepath.Join(root, "reloads")), "reload"); got != want {
			t.Fatalf("settings reloads: %d, want %d", got, want)
		}
	}

	// A session started by an earlier version has no settings manager pid:
	// it keeps 96 DPI, reports no density, and only changes its size.
	if output, err := resize("2"); err != nil || output != "" || read(filepath.Join(run, "xsettings.conf")) != "" {
		t.Fatalf("fixed density changed: %q %v", output, err)
	}
	if modes := read(filepath.Join(root, "modes")); !strings.Contains(modes, ":3 "+run+"/Xauthority --output VNC-0 --mode cage-1000x800") {
		t.Fatalf("size not applied on the instance display: %q", modes)
	}
	if entered := read(filepath.Join(root, "sudo")); !strings.HasPrefix(entered, "-n nsenter --target 4242 --mount --net --setuid ") {
		t.Fatalf("mode commands did not enter the instance's namespaces: %q", entered)
	}

	manager := exec.Command(filepath.Join(bin, "xsettingsd"))
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Process.Kill(); _, _ = manager.Process.Wait() })
	for attempt := 0; read("/proc/"+strconv.Itoa(manager.Process.Pid)+"/comm") != "xsettingsd\n"; attempt++ {
		if attempt == 100 {
			t.Skip("the stand-in manager is not visible under its name")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	write(filepath.Join(run, "xsettings.pid"), strconv.Itoa(manager.Process.Pid)+"\n", 0o600)
	write(filepath.Join(run, "base-display-scale"), "2\n", 0o600)
	write(filepath.Join(run, "display-scale"), "2\n", 0o600)
	write(filepath.Join(run, "xsettings.fixed"), "Gtk/DecorationLayout \":\"\n", 0o600)

	if output, err := resize("1.5"); err != nil || output != "scale 1.5\n" {
		t.Fatalf("applied density not reported: %q %v", output, err)
	}
	if conf := read(filepath.Join(run, "xsettings.conf")); conf != "Xft/DPI 147456\nGdk/WindowScalingFactor 1\nGdk/UnscaledDPI 147456\nGtk/DecorationLayout \":\"\n" {
		t.Fatalf("settings: %q", conf)
	}
	if read(filepath.Join(run, "display-scale")) != "1.5\n" {
		t.Fatal("density not recorded")
	}
	reloads(1)
	resize("1.5")
	reloads(1)
	// The phone's hold on the default display does not apply to instances.
	if _, err := os.Stat(filepath.Join(run, "desktop-size")); !os.IsNotExist(err) {
		t.Fatal("instance recorded a size for the phone view")
	}

	// A stopped instance changes nothing.
	write(filepath.Join(root, "main-pid"), "0\n", 0o600)
	modes := read(filepath.Join(root, "modes"))
	if _, err := resize("1"); err == nil || read(filepath.Join(root, "modes")) != modes || read(filepath.Join(run, "display-scale")) != "1.5\n" {
		t.Fatal("a stopped instance was resized")
	}
	reloads(1)
}
