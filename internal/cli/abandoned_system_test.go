package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/hostimage"
	"github.com/sylvinus/agent-vm/internal/vm"
	"github.com/sylvinus/agent-vm/internal/vmname"
)

// An environment an earlier version created from Ubuntu must not be booted:
// this version's setup only fits Debian. Its VM and files stay as they are.
func TestStartRefusesEnvironmentCreatedFromUbuntu(t *testing.T) {
	se := newStartEnv(t)
	se.base()
	if code := se.run("run", "true"); code != 0 {
		t.Fatalf("initial start: %d %s", code, se.out())
	}
	name := vmname.Name(se.proj)
	original := se.fake.VMs[name]
	original.Status = vm.Stopped
	media := filepath.Join(string(se.state), "guest-media")
	plan := hostimage.Plan{
		Host:    hostimage.Identity{System: "ubuntu-22.04", Version: "22.04", Arch: "amd64"},
		Version: "22.04", Format: "qcow2", Path: filepath.Join(media, "ubuntu-22.04", "image.qcow2"),
		Digest: "sha256:" + strings.Repeat("a", 64),
	}
	if err := hostimage.Save(media, plan); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(media, "host-system-source.json")
	before, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if code := se.run("run", "true"); code == 0 || !strings.Contains(se.out(), "Ubuntu") {
		t.Fatalf("Ubuntu environment was started: %d %s", code, se.out())
	}
	// Each run starts with an empty call log.
	if se.fake.VMs[name] != original || original.Status != vm.Stopped || se.fake.CallLog() != "" {
		t.Fatalf("refusal acted on the VM: %s", se.fake.CallLog())
	}
	if after, err := os.ReadFile(record); err != nil || string(after) != string(before) {
		t.Fatal("refusal changed the saved image choice", err)
	}
}
