package qemu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
)

func TestMacOSViewStopsWhenQEMUExits(t *testing.T) {
	oldView, oldOwns := MacOSOffscreenView, OwnsMacOSOffscreenView
	t.Cleanup(func() { MacOSOffscreenView, OwnsMacOSOffscreenView = oldView, oldOwns })
	started := make(chan struct{})
	MacOSOffscreenView = func(ctx context.Context, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	OwnsMacOSOffscreenView = func(string) bool { return true }
	cfg, _ := macOSFixture(t)
	driver := &LimaQemuDriver{Instance: &limatype.Instance{Dir: cfg.InstanceDir, Config: cfg.LimaYAML}, qDone: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- driver.RunGUI(ctx) }()
	<-started
	close(driver.qDone)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("QEMU exited but its login view kept the hostagent alive")
	}
}

func macOSFixture(t *testing.T) (Config, *limatype.QEMUMacOSOpts) {
	t.Helper()
	dir := t.TempDir()
	for _, file := range []string{filenames.QemuEfiCodeFD, MacOSVariables, MacOSBootDisk, filenames.Disk, filenames.CIDataISO, MacOSInstaller, MacOSInstallPackage} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fd.Close() })
	value := func(s string) *string { return &s }
	cpus := 4
	y := &limatype.LimaYAML{OS: value(limatype.DARWIN), Arch: value(limatype.X8664), VMType: value(limatype.QEMU), CPUs: &cpus, Memory: value("8GiB")}
	y.Video.Display = value("offscreen")
	return Config{Name: "mac", InstanceDir: dir, LimaYAML: y, UsernetDgram: fd}, &limatype.QEMUMacOSOpts{UUID: "4cf5e572-6e50-43f1-887f-116a16278861"}
}

func TestMacOSArgumentsRequireControlledNetworkAndPrivateDisks(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(Config, *limatype.QEMUMacOSOpts) Config
	}{
		{"no-netstack", func(c Config, _ *limatype.QEMUMacOSOpts) Config { c.UsernetDgram = nil; return c }},
		{"extra-network", func(c Config, _ *limatype.QEMUMacOSOpts) Config {
			c.LimaYAML.Networks = []limatype.Network{{Socket: "/tmp/escape"}}
			return c
		}},
		{"host-share", func(c Config, _ *limatype.QEMUMacOSOpts) Config {
			c.LimaYAML.Mounts = []limatype.Mount{{Location: "/Users"}}
			return c
		}},
		{"port-forward", func(c Config, _ *limatype.QEMUMacOSOpts) Config {
			c.LimaYAML.PortForwards = []limatype.PortForward{{Ignore: false}}
			return c
		}},
		{"invalid-identity", func(c Config, o *limatype.QEMUMacOSOpts) Config { o.UUID = "missing"; return c }},
		{"linked-system-disk", func(c Config, _ *limatype.QEMUMacOSOpts) Config {
			path := filepath.Join(c.InstanceDir, filenames.Disk)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(c.InstanceDir, MacOSBootDisk), path); err != nil {
				t.Fatal(err)
			}
			return c
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, options := macOSFixture(t)
			if _, err := macOSArguments(test.mutate(cfg, options), options); err == nil {
				t.Fatal("unsafe macOS launch accepted")
			}
		})
	}
}

func TestMacOSArgumentsKeepOneNetworkForInstallerAndRuntime(t *testing.T) {
	for _, install := range []bool{true, false} {
		cfg, options := macOSFixture(t)
		options.Installer = install
		args, err := macOSArguments(cfg, options)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-name lima-"+cfg.Name+" ") {
			t.Fatal("macOS process must retain Lima's verifiable instance name")
		}
		if !strings.Contains(joined, "-pidfile "+filepath.Join(cfg.InstanceDir, filenames.PIDFile(limatype.QEMU))) {
			t.Fatal("macOS lifecycle cannot identify its running QEMU process")
		}
		if !strings.Contains(joined, "ICH9-LPC.disable_s3=1") || !strings.Contains(joined, "ICH9-LPC.disable_s4=1") {
			t.Fatal("background macOS guest can enter a suspended power state")
		}
		if strings.Count(joined, "-netdev ") != 1 || strings.Count(joined, "netdev=net0") != 1 || !strings.Contains(joined, "dgram,id=net0,local.type=fd") {
			t.Fatalf("macOS escaped the per-instance datagram network: %s", joined)
		}
		for _, forbidden := range []string{"-netdev user", "hostfwd=", "vmnet", "-vnc", "-display cocoa", "osk=", "vvfat"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("unexpected unmanaged device: %s", forbidden)
			}
		}
		if !install && !strings.Contains(joined, "apple-gfx-pci") {
			t.Fatal("runtime lost accelerated graphics")
		}
		if !strings.Contains(joined, "snapshot=on,file="+filepath.Join(cfg.InstanceDir, MacOSBootDisk)) {
			t.Fatal("verified boot image must be protected by a disposable overlay")
		}
		if install && (!strings.Contains(joined, "-vga std") || !strings.Contains(joined, "snapshot=on,file="+filepath.Join(cfg.InstanceDir, MacOSInstaller))) {
			t.Fatal("installer is not protected by a disposable overlay with its recovery framebuffer")
		}
	}
}
