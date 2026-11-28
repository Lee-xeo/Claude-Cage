package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/host"
)

func TestResourceAllocationSaveAndStartup(t *testing.T) {
	oldMem, oldCPU := host.MemGiB, host.CPUs
	oldAvailable := host.WindowsAvailableMemory
	t.Cleanup(func() { host.WindowsAvailableMemory = oldAvailable })
	host.WindowsAvailableMemory = func() uint64 { return 16 << 30 }
	t.Cleanup(func() { host.MemGiB, host.CPUs = oldMem, oldCPU })
	host.MemGiB = func() int { return 24 }
	host.CPUs = func() int { return 10 }
	t.Setenv("AGENT_VM_HOST_SHARE", "2")
	s := testServer(t)
	check := func(memory, cpus int) {
		t.Helper()
		m, c, err := cage.ConfiguredResourceLimits(s.configPath)
		if err != nil || m != memory || c != cpus {
			t.Fatalf("startup limits = %d/%d, %v", m, c, err)
		}
	}
	check(8, 5)
	for _, body := range []string{
		`{"allocation":{"automatic":false,"memory_gib":0,"cpus":2}}`,
		`{"allocation":{"automatic":false,"memory_gib":13,"cpus":2}}`,
		`{"allocation":{"automatic":false,"memory_gib":4,"cpus":6}}`,
		`{"allocation":{"automatic":false,"memory_gib":4.5,"cpus":2}}`,
	} {
		if w := request(s, "POST", "/api/environment/resources", s.origin, body, true); w.Code == 200 {
			t.Fatalf("accepted invalid allocation: %s", body)
		}
		check(8, 5)
	}
	before, _ := os.ReadFile(s.configPath)
	if w := request(s, "POST", "/api/environment/resources", s.origin, `{"allocation":{"automatic":false,"memory_gib":4,"cpus":2}}`, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	check(4, 2)
	after, _ := os.ReadFile(s.configPath)
	if string(before) != string(after) {
		t.Fatal("resource save changed network configuration")
	}
	w := request(s, "GET", "/api/environment/resources", "", "", true)
	var status struct {
		Automatic  bool `json:"automatic"`
		Memory     int  `json:"memory_gib"`
		CPUs       int  `json:"cpus"`
		Disk       int  `json:"disk_gib"`
		MinDiskGiB int  `json:"min_disk_gib"`
		MaxDiskGiB int  `json:"max_disk_gib"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || status.Automatic || status.Memory != 4 || status.CPUs != 2 || status.Disk != cage.DefaultDiskGiB || status.MinDiskGiB < cage.MinDiskGiB || status.MaxDiskGiB < 64 {
		t.Fatal(w.Body.String(), err)
	}
	// Saving a custom disk size within range succeeds and persists; out-of-range fails.
	if w = request(s, "POST", "/api/environment/resources", s.origin, `{"allocation":{"automatic":false,"memory_gib":4,"cpus":2,"disk_gib":48}}`, true); w.Code != 200 {
		t.Fatalf("valid disk allocation rejected: %d %s", w.Code, w.Body.String())
	}
	if disk, err := cage.ConfiguredDiskLimit(s.configPath); err != nil || disk != 48 {
		t.Fatalf("saved disk limit = %d, %v; want 48", disk, err)
	}
	if w = request(s, "POST", "/api/environment/resources", s.origin, `{"allocation":{"automatic":false,"memory_gib":4,"cpus":2,"disk_gib":4}}`, true); w.Code == 200 {
		t.Fatalf("accepted disk below minimum: %s", w.Body.String())
	}
	s.op.Lock()
	w = request(s, "POST", "/api/environment/resources", s.origin, `{"allocation":{"automatic":true}}`, true)
	s.op.Unlock()
	if w.Code != 409 {
		t.Fatal("save must not race an environment operation", w.Code)
	}
	check(4, 2)
	if w = request(s, "POST", "/api/environment/resources", s.origin, `{"allocation":{"automatic":true}}`, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	check(8, 5)
}

func TestWindowsAutomaticMemoryForStoppedAndRunningVM(t *testing.T) {
	oldMem, oldCPU, oldAvailable := host.MemGiB, host.CPUs, host.WindowsAvailableMemory
	t.Cleanup(func() { host.MemGiB, host.CPUs, host.WindowsAvailableMemory = oldMem, oldCPU, oldAvailable })
	t.Setenv("AGENT_VM_HOST_SHARE", "2")
	host.MemGiB = func() int { return 15 }
	host.CPUs = func() int { return 32 }
	host.WindowsAvailableMemory = func() uint64 { return 6042 << 20 }
	s := testServer(t)
	if err := writePrivate(filepath.Join(s.profile, "environment.json"), cage.EnvironmentRecord{Directory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	check := func(wantMemory int, wantRestart bool) {
		t.Helper()
		w := request(s, "GET", "/api/environment/resources", "", "", true)
		var data struct {
			CPUs        int   `json:"cpus"`
			Memory      int   `json:"memory_gib"`
			Recommended int64 `json:"recommended_memory_bytes"`
			Restart     bool  `json:"restart_required"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || data.CPUs != 16 || data.Memory != wantMemory || data.Recommended != int64(wantMemory)<<30 || data.Restart != wantRestart {
			t.Fatalf("unexpected resource settings: %s (%v)", w.Body, err)
		}
	}
	check(4, false)
	dir := s.instanceDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte("memory: 4GiB\ncpus: 16\ndisk: 24GiB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(dir, "cage-running.json"), map[string]int{"pid": os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	host.WindowsAvailableMemory = func() uint64 { t.Fatal("sampled available memory while the VM was running"); return 0 }
	for _, automatic := range []bool{true, false} {
		status := cage.ResourceStatus{Version: 1, UpdatedAt: time.Now(), Maximum: 4 << 30, CPUs: 16, AutomaticAtStart: automatic}
		if err := writePrivate(filepath.Join(dir, "cage-resources.json"), status); err != nil {
			t.Fatal(err)
		}
		if automatic {
			check(4, false)
		} else {
			// The running VM was manually sized before the user restored auto.
			check(7, true)
		}
	}
}
