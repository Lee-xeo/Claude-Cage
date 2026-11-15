package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
)

func TestStopProxyLifecycle(t *testing.T) {
	if os.Getenv("CAGE_TEST_STOP_PROXY") == "1" {
		os.Args = append([]string{"cage"}, os.Args[slices.Index(os.Args, "--")+1:]...)
		os.Exit(run())
	}
	for _, keepProxy := range []bool{true, false} {
		name := "explicit stop"
		if keepProxy {
			name = "internal restart"
		}
		t.Run(name, func(t *testing.T) {
			profile, err := os.MkdirTemp("", "stop-proxy-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(profile) })
			// A stopped Lima instance exercises the real CLI dispatch and stop
			// path without booting a VM or accessing the user's profile.
			instance := filepath.Join(profile, "state", "lima", "restart-test")
			proxy := filepath.Join(profile, "proxy")
			bin := filepath.Join(profile, "bin")
			for _, dir := range []string{instance, proxy, bin} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{
				filepath.Join(instance, "lima.yaml"):          "vmType: qemu\narch: aarch64\nimages:\n- location: https://example.com/unused.img\n",
				filepath.Join(proxy, "managed-service.plist"): "isolated test service",
				filepath.Join(bin, "launchctl"):               "#!/bin/sh\nif [ \"$1\" = bootout ]; then touch \"$CLAUDE_CAGE_CONFIG.proxy-stopped\"; fi\nexit 0\n",
			}
			for path, data := range files {
				if err := os.WriteFile(path, []byte(data), 0700); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"-test.run=^TestStopProxyLifecycle$", "--", "stop"}
			if keepProxy {
				args = append(args, "--keep-proxy")
			}
			args = append(args, "restart-test")
			cmd := exec.Command(os.Args[0], args...)
			config := filepath.Join(profile, "config.json")
			cmd.Env = append(os.Environ(), "CAGE_TEST_STOP_PROXY=1", cage.ConfigEnv+"="+config, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("stop failed: %v\n%s", err, output)
			}
			_, err = os.Stat(config + ".proxy-stopped")
			if keepProxy && !os.IsNotExist(err) || !keepProxy && err != nil {
				t.Fatalf("proxy lifetime did not match stop intent: %v", err)
			}
		})
	}
}
