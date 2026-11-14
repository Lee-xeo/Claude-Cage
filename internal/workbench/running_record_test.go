package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/guestos"
)

func TestIntelMacRunningRequiresQEMUProcess(t *testing.T) {
	s := testServer(t)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "cage", Backend: guestos.MacOSQEMU}
	dir := s.instanceDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RecordRunningAt(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "qemu.pid")
	for _, value := range []string{"", "not-a-pid", "0", "-1", "1", "999999999", strings.Repeat("2", 33)} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if s.vmRunning() {
			t.Fatalf("live hostagent with invalid or exited QEMU %q was treated as running", value)
		}
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !s.vmRunning() {
		t.Fatal("live QEMU process was not recognized")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if s.vmRunning() {
		t.Fatal("QEMU exit left the environment running")
	}
}

func TestVMRecordWithReusedPIDIsNotRunning(t *testing.T) {
	for _, guest := range []string{"linux", "macos"} {
		t.Run(guest, func(t *testing.T) {
			s := testServer(t)
			s.preferences.Workspace, _ = filepath.EvalSymlinks(t.TempDir())
			if guest == "macos" {
				s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "cage"}
			}
			dir := s.instanceDir()
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(s.configPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.RecordRunningAt(dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "cage-running.json")
			var record map[string]any
			b, _ := os.ReadFile(path)
			if err := json.Unmarshal(b, &record); err != nil {
				t.Fatal(err)
			}
			started, _ := record["started"].(string)
			if started == "" {
				t.Skip("process start time is unavailable on this platform")
			}
			if !s.vmRunning() {
				t.Fatal("current owner was not recognized")
			}
			if owners, err := controllerVMOwners(s.profile); err != nil || len(owners) != 1 {
				t.Fatalf("owners = %v, %v", owners, err)
			}
			record["started"] = started + "0"
			if err := writePrivate(path, record); err != nil {
				t.Fatal(err)
			}
			if s.vmRunning() {
				t.Fatal("record from an earlier process with the same PID was treated as running")
			}
			if owners, err := controllerVMOwners(s.profile); err != nil || len(owners) != 0 {
				t.Fatalf("stale owners = %v, %v", owners, err)
			}
			delete(record, "started")
			if err := writePrivate(path, record); err != nil {
				t.Fatal(err)
			}
			if !s.vmRunning() {
				t.Fatal("legacy PID-only record was rejected")
			}
		})
	}
}
