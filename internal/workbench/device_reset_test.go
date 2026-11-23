package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lima-vm/lima/v2/pkg/guestidentity"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"github.com/sylvinus/agent-vm/internal/vm"
	"github.com/sylvinus/agent-vm/internal/vmname"
)

func waitDeviceResetJob(t *testing.T, s *Server) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := s.jobs.snapshot()
		if !snap.Running {
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("device reset job timed out")
	return Job{}
}

func TestDeviceResetPythonScript(t *testing.T) {
	py := ""
	for _, candidate := range []string{"python3", "python"} {
		resolved, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if exec.Command(resolved, "-c", "import sys; raise SystemExit(0 if sys.version_info[0] == 3 else 1)").Run() == nil {
			py = resolved
			break
		}
	}
	if py == "" {
		t.Skip("python3 not found")
	}
	cmd := exec.Command(py, "-m", "unittest", "device_reset_test.py")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("device_reset_test.py failed: %v\n%s", err, out)
	}
}

func TestDeviceResetAuthAndValidation(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		method, path, body string
		handler            http.HandlerFunc
	}{
		{http.MethodGet, "/api/environment/device-reset", "", s.deviceResetStatusHandler},
		{http.MethodPost, "/api/environment/device-reset/preview", `{}`, s.deviceResetPreviewHandler},
		{http.MethodPost, "/api/environment/device-reset/execute", `{"mode":"quick","items":[]}`, s.deviceResetExecuteHandler},
		{http.MethodPost, "/api/environment/device-reset/restore", `{"kind":"archive","id":"deadbeef1234"}`, s.deviceResetRestoreHandler},
		{http.MethodPost, "/api/environment/device-reset/delete-archive", `{"kind":"archive","id":"deadbeef1234"}`, s.deviceResetDeleteArchiveHandler},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, s.origin+tc.path, strings.NewReader(tc.body))
		tc.handler(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s untrusted status = %d, want 403", tc.method, tc.path, w.Code)
		}
	}

	// Verify validation rejects irreversible without explicit confirmation
	s.ready = true
	w := desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute", []byte(`{"mode":"quick","items":["browser_profile"],"irreversible":true,"confirm_irreversible":false}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("irreversible without confirm = %d, want 400", w.Code)
	}
	// Invalid item ID rejected
	w = desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute", []byte(`{"mode":"quick","items":["../evil"]}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid item = %d, want 400", w.Code)
	}
	// Quick mode requires running environment
	s.ready = false
	w = desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute", []byte(`{"mode":"quick","items":["browser_profile"]}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("quick mode when stopped = %d, want 409", w.Code)
	}
}

func TestDeviceResetQuickModeEndToEnd(t *testing.T) {
	s := testServer(t)
	profileDir := s.profile
	s.ready = true
	origProfile, err := guestprofile.Create(profileDir, guestprofile.Options{
		Hostname:        "OLD-HOST1",
		Username:        "alex42",
		BrowserUsername: "robin19",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldID, err := guestidentity.New()
	if err != nil {
		t.Fatal(err)
	}
	oldID.Hostname = origProfile.Hostname

	var rotatedTo *guestidentity.Identity
	var guestPayload map[string]any
	stopped := false
	restarted := false

	s.deviceReset = deviceResetHooks{
		currentLima: func(_ context.Context, _ string) (vm.IdentityInfo, error) {
			return vm.IdentityInfo{VMType: "vz", Identity: oldID, HasVZIdentifier: true}, nil
		},
		rotateLima: func(_ context.Context, _ string, next *guestidentity.Identity) (*guestidentity.Identity, bool, error) {
			rotatedTo = next
			return oldID, true, nil
		},
		stopEnv:  func() error { stopped = true; return nil },
		startEnv: func(bool) error { restarted = true; return nil },
		runGuestReset: func(_ context.Context, req map[string]any) ([]byte, error) {
			if req["action"] == "inspect" {
				return json.Marshal(guestResetInspectResponse{
					Version:         1,
					Hostname:        origProfile.Hostname,
					MachineIDPrefix: "11223344",
					Items:           defaultDeviceResetTargets(),
					Batches:         []deviceResetBatch{},
				})
			}
			guestPayload = req
			return json.Marshal(guestResetActionResponse{
				Status:          "ok",
				BatchID:         strings.Repeat("a", 32),
				Irreversible:    false,
				OSRotated:       true,
				Hostname:        req["hostname"].(string),
				MachineIDPrefix: req["machine_id"].(string)[:8],
				Outcomes: []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Reason string `json:"reason"`
				}{
					{ID: "browser_profile", Status: "cleaned", Reason: "已清理"},
					{ID: "claude_code", Status: "cleaned", Reason: "已清理"},
				},
			})
		},
	}

	w := desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute",
		[]byte(`{"mode":"quick","items":["browser_profile","claude_code"],"irreversible":false}`))
	if w.Code != http.StatusOK {
		t.Fatalf("execute quick status = %d: %s", w.Code, w.Body.String())
	}
	job := waitDeviceResetJob(t, s)
	if job.Error != "" {
		t.Fatalf("quick reset failed: %s", job.Error)
	}
	if !stopped || !restarted || rotatedTo == nil {
		t.Fatalf("expected stop=%v restart=%v rotatedTo=%v", stopped, restarted, rotatedTo)
	}
	if guestPayload["old_mac"] != oldID.MACAddress || guestPayload["new_mac"] != rotatedTo.MACAddress {
		t.Fatalf("MAC handoff mismatch: %+v", guestPayload)
	}
	updatedProfile, err := guestprofile.Load(profileDir)
	if err != nil {
		t.Fatal(err)
	}
	if updatedProfile.Username != origProfile.Username || updatedProfile.BrowserUsername != origProfile.BrowserUsername {
		t.Fatalf("quick reset modified Linux accounts: %+v", updatedProfile)
	}
	if updatedProfile.Hostname == origProfile.Hostname || updatedProfile.ExtensionSuffix == origProfile.ExtensionSuffix {
		t.Fatalf("quick reset did not rotate mutable profile fields: %+v", updatedProfile)
	}
	last := s.loadLastDeviceResetResult()
	if last == nil || last.Mode != "quick" || !last.VZIdentifierReset || last.BatchID != strings.Repeat("a", 32) {
		t.Fatalf("unexpected last result: %+v", last)
	}
	if last.Before.Hostname != "OLD-HOST1" || last.After.Hostname != updatedProfile.Hostname {
		t.Fatalf("before/after mismatch: %+v", last)
	}
}

func seedFakeLimaEnvironment(t *testing.T, profileDir string, host string) (string, string, string) {
	t.Helper()
	if _, err := guestprofile.Create(profileDir, guestprofile.Options{
		Hostname:        host,
		Username:        "olduser1",
		BrowserUsername: "oldweb2",
	}); err != nil {
		t.Fatal(err)
	}
	instName := cage.InstanceName(profileDir, environmentWorkspace(profileDir))
	limaHome := filepath.Join(profileDir, "state", "lima")
	instDir := filepath.Join(limaHome, instName)
	baseDir := filepath.Join(limaHome, vmname.Template)
	for _, d := range []string{instDir, baseDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "marker.txt"), []byte("old-disk-"+filepath.Base(d)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stateDir := filepath.Join(profileDir, "state")
	if err := os.WriteFile(filepath.Join(stateDir, ".agent-vm-base-version"), []byte("100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, ".agent-vm-base-built-by"), []byte("0.1.8\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return instName, instDir, baseDir
}

func TestDeviceResetRebuildRecoverableRestoreAndDelete(t *testing.T) {
	s := testServer(t)
	profileDir := s.profile
	_, instDir, baseDir := seedFakeLimaEnvironment(t, profileDir, "OLD-BOX1")

	s.deviceReset = deviceResetHooks{
		stopEnv: func() error { return nil },
		startEnv: func(bool) error {
			// Simulate fresh VM created during rebuild
			_ = os.MkdirAll(instDir, 0700)
			_ = os.WriteFile(filepath.Join(instDir, "marker.txt"), []byte("new-disk"), 0600)
			_ = os.MkdirAll(baseDir, 0700)
			_ = os.WriteFile(filepath.Join(baseDir, "marker.txt"), []byte("new-base"), 0600)
			return nil
		},
	}

	// Execute L3 rebuild with custom hostname (recoverable default)
	w := desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute",
		[]byte(`{"mode":"rebuild","items":["browser_profile"],"irreversible":false,"custom_profile":{"hostname":"NEW-CUSTOM1","username":"newdev9","browser_username":"newweb8"}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("execute rebuild = %d: %s", w.Code, w.Body.String())
	}
	job := waitDeviceResetJob(t, s)
	if job.Error != "" {
		t.Fatalf("rebuild failed: %s", job.Error)
	}

	p, err := guestprofile.Load(profileDir)
	if err != nil || p == nil || p.Hostname != "NEW-CUSTOM1" || p.Username != "newdev9" {
		t.Fatalf("new profile not applied: %+v, %v", p, err)
	}
	archives := s.listDeviceResetArchives()
	if len(archives) != 1 || archives[0].PreviousHostname != "OLD-BOX1" || !archives[0].HasInstance || !archives[0].HasBaseTemplate {
		t.Fatalf("expected 1 ready archive, got %+v", archives)
	}
	archiveID := archives[0].ID

	// Restore archived old VM
	w = desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/restore",
		[]byte(`{"kind":"archive","id":"`+archiveID+`"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("restore archive = %d: %s", w.Code, w.Body.String())
	}
	job = waitDeviceResetJob(t, s)
	if job.Error != "" {
		t.Fatalf("restore failed: %s", job.Error)
	}
	restoredProfile, err := guestprofile.Load(profileDir)
	if err != nil || restoredProfile == nil || restoredProfile.Hostname != "OLD-BOX1" || restoredProfile.Username != "olduser1" {
		t.Fatalf("old profile not restored: %+v, %v", restoredProfile, err)
	}
	// The replaced new environment must be kept as a rollback target, never discarded.
	left := s.listDeviceResetArchives()
	if len(left) != 1 || left[0].ID == archiveID || left[0].PreviousHostname != "NEW-CUSTOM1" || !left[0].HasInstance {
		t.Fatalf("restore must archive the replaced environment, got %+v", left)
	}
	if data, err := os.ReadFile(filepath.Join(s.deviceResetArchivesRoot(), left[0].ID, "lima-instance", "marker.txt")); err != nil || string(data) != "new-disk" {
		t.Fatalf("replaced instance data lost: %q %v", data, err)
	}
}

func TestDeviceResetRebuildAutoRollbackOnFailure(t *testing.T) {
	s := testServer(t)
	profileDir := s.profile
	_, instDir, baseDir := seedFakeLimaEnvironment(t, profileDir, "KEEP-OLD1")

	s.deviceReset = deviceResetHooks{
		stopEnv: func() error { return nil },
		startEnv: func(bool) error {
			// Simulate partial new VM creation followed by failure
			_ = os.MkdirAll(instDir, 0700)
			_ = os.WriteFile(filepath.Join(instDir, "marker.txt"), []byte("broken-new"), 0600)
			return errors.New("simulated setup failure")
		},
	}

	w := desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute",
		[]byte(`{"mode":"rebuild","items":[],"irreversible":true,"confirm_irreversible":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("execute rebuild = %d: %s", w.Code, w.Body.String())
	}
	job := waitDeviceResetJob(t, s)
	if job.Error == "" {
		t.Fatal("expected rebuild job error")
	}

	// Verify old VM, old base, old profile are completely restored even though irreversible=true!
	p, err := guestprofile.Load(profileDir)
	if err != nil || p == nil || p.Hostname != "KEEP-OLD1" || p.Username != "olduser1" {
		t.Fatalf("rollback failed to restore profile: %+v, %v", p, err)
	}
	instData, err := os.ReadFile(filepath.Join(instDir, "marker.txt"))
	if err != nil || !strings.HasPrefix(string(instData), "old-disk-") {
		t.Fatalf("rollback failed to restore instance disk: %q, %v", string(instData), err)
	}
	baseData, err := os.ReadFile(filepath.Join(baseDir, "marker.txt"))
	if err != nil || !strings.HasPrefix(string(baseData), "old-disk-") {
		t.Fatalf("rollback failed to restore base disk: %q, %v", string(baseData), err)
	}
	if len(s.listDeviceResetArchives()) != 0 {
		t.Fatal("failed rebuild left stale archive behind")
	}
}

func TestDeviceResetRebuildIrreversibleDeletesArchiveAfterSuccess(t *testing.T) {
	s := testServer(t)
	profileDir := s.profile
	_, instDir, _ := seedFakeLimaEnvironment(t, profileDir, "OLD-GONE1")

	s.deviceReset = deviceResetHooks{
		stopEnv: func() error { return nil },
		startEnv: func(bool) error {
			// While startEnv is running, the staging archive must still exist for rollback!
			entries, _ := os.ReadDir(s.deviceResetArchivesRoot())
			if len(entries) != 1 {
				t.Errorf("expected staging archive during rebuild, found %d", len(entries))
			}
			_ = os.MkdirAll(instDir, 0700)
			return nil
		},
	}

	w := desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute",
		[]byte(`{"mode":"rebuild","items":["browser_profile"],"irreversible":true,"confirm_irreversible":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("execute rebuild = %d: %s", w.Code, w.Body.String())
	}
	job := waitDeviceResetJob(t, s)
	if job.Error != "" {
		t.Fatalf("irreversible rebuild failed: %s", job.Error)
	}
	if entries, _ := os.ReadDir(s.deviceResetArchivesRoot()); len(entries) != 0 {
		t.Fatalf("expected archive deleted after irreversible rebuild success, got %d entries", len(entries))
	}
	last := s.loadLastDeviceResetResult()
	if last == nil || !last.Irreversible || last.ArchiveID != "" {
		t.Fatalf("unexpected last result: %+v", last)
	}
}

func TestDeviceResetStatusPreviewAndDeleteArchive(t *testing.T) {
	s := testServer(t)
	profileDir := s.profile
	_, instDir, _ := seedFakeLimaEnvironment(t, profileDir, "STAT-BOX1")

	s.deviceReset = deviceResetHooks{
		stopEnv:  func() error { return nil },
		startEnv: func(bool) error { _ = os.MkdirAll(instDir, 0700); return nil },
	}
	// GET status
	w := desktopRequestForTest(s, http.MethodGet, "/api/environment/device-reset", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", w.Code, w.Body.String())
	}
	var st deviceResetStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Supported || st.Current.Hostname != "STAT-BOX1" || len(st.Targets) != 5 || len(st.Protected) == 0 {
		t.Fatalf("unexpected status payload: %+v", st)
	}

	// POST preview
	w = desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/preview", []byte(`{"hostname":"PREVIEW-1"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("POST preview = %d: %s", w.Code, w.Body.String())
	}
	var preview guestprofile.Options
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.Hostname != "PREVIEW-1" || preview.Username == "" {
		t.Fatalf("unexpected preview: %+v, %v", preview, err)
	}

	// Rebuild recoverable -> create archive -> delete archive via HTTP
	w = desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/execute", []byte(`{"mode":"rebuild","items":[]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("execute rebuild = %d: %s", w.Code, w.Body.String())
	}
	if job := waitDeviceResetJob(t, s); job.Error != "" {
		t.Fatalf("rebuild failed: %s", job.Error)
	}
	archives := s.listDeviceResetArchives()
	if len(archives) != 1 {
		t.Fatalf("expected 1 archive, got %d", len(archives))
	}
	w = desktopRequestForTest(s, http.MethodPost, "/api/environment/device-reset/delete-archive", []byte(`{"kind":"archive","id":"`+archives[0].ID+`"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("delete-archive = %d: %s", w.Code, w.Body.String())
	}
	if len(s.listDeviceResetArchives()) != 0 {
		t.Fatal("archive still present after delete-archive")
	}
}
