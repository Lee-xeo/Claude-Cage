package workbench

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storageRecoveryServer(t *testing.T) *Server {
	t.Helper()
	s := testServer(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := writePrivate(filepath.Join(s.profile, "environment.json"), map[string]string{"directory": workspace}); err != nil {
		t.Fatal(err)
	}
	return s
}

// storageRecoveryJournal leaves the migration journal of a removed Windows
// guest: a transaction only that guest's runtime could have resolved.
func storageRecoveryJournal(t *testing.T, s *Server) string {
	t.Helper()
	instance := filepath.Join(s.profile, "state", "guest-os", "synthetic-vm")
	id := strings.Repeat("a", 32)
	target := filepath.Join(t.TempDir(), "destination")
	journal := filepath.Join(s.profile, "environment-storage-pending.json")
	if err := writePrivate(journal, map[string]any{
		"Next":    map[string]any{"Directory": target, "ID": id},
		"Runtime": "windows-hyperv",
		"Transition": map[string]any{
			"id": id, "source": filepath.Join(s.profile, "state"),
			"destination": filepath.Join(target, "state"), "backup": filepath.Join(s.profile, "state-backup-"+id),
			"state": map[string]any{
				"instance": instance,
				"disk":     map[string]any{"path": filepath.Join(instance, "disk.vhdx"), "volume": 1, "file_id": 2},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return journal
}

func TestStorageRecoveryKeepsHostAccessAndBlocksEnvironmentMutations(t *testing.T) {
	s := storageRecoveryServer(t)
	s.ready = true
	s.networkIntentError = "synthetic independent network failure"
	s.failStorageRecovery(errors.New("synthetic system manager unavailable"))
	for _, endpoint := range []string{"/api/account", "/api/status", "/api/settings", "/api/environment/storage", "/api/environment/initialization", "/api/diagnostics", "/api/diagnostics/export"} {
		response := desktopRequestForTest(s, http.MethodGet, endpoint, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("host access %s returned %d %s", endpoint, response.Code, response.Body.String())
		}
		if endpoint == "/api/status" {
			var view struct {
				Ready           bool                `json:"ready"`
				NetworkError    string              `json:"network_error"`
				StorageRecovery storageRecoveryView `json:"storage_recovery"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if view.Ready || !view.StorageRecovery.Required || view.StorageRecovery.Error != "synthetic system manager unavailable" || view.NetworkError != s.networkIntentError {
				t.Fatal("recovery did not preserve the independent causes", view)
			}
		}
		if endpoint == "/api/diagnostics/export" {
			if _, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len())); err != nil {
				t.Fatal("pending recovery prevented a valid diagnostic export", err)
			}
		}
	}
	for _, endpoint := range []string{
		"/api/start", "/api/stop", "/api/settings", "/api/projects/open", "/api/environment/resources",
		"/api/environment/storage", "/api/environment/update", "/api/environment/initialization",
		"/api/browser/open", "/api/input", "/api/input/paste/start", "/api/account/logout",
	} {
		response := desktopRequestForTest(s, http.MethodPost, endpoint, []byte(`{}`))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "synthetic system manager unavailable") {
			t.Fatalf("mutation %s reached its receiver: %d %s", endpoint, response.Code, response.Body.String())
		}
	}
	for _, endpoint := range []string{"/api/browser/state", "/api/vnc", "/api/terminals", "/api/audio", "/api/audio/input"} {
		if response := desktopRequestForTest(s, http.MethodGet, endpoint, nil); response.Code != http.StatusConflict {
			t.Fatalf("guest session %s was admitted: %d", endpoint, response.Code)
		}
	}
	if err := s.attach(); err == nil || !strings.Contains(err.Error(), "synthetic system manager unavailable") {
		t.Fatal("periodic attach bypassed recovery", err)
	}
	if err := s.startEnvironment(false); err == nil || !strings.Contains(err.Error(), "synthetic system manager unavailable") {
		t.Fatal("direct start bypassed recovery", err)
	}
	if _, err := s.submitJob("network-recovery", func() error { t.Error("background mutation ran"); return nil }); err == nil {
		t.Fatal("background job bypassed recovery")
	}
	var skipped int
	s.runNetworkMonitorTick(context.Background(), &skipped)
	if s.jobs.snapshot().ID != "" {
		t.Fatal("recovery gate created an environment job")
	}
}

func TestStorageRecoverySuccessfulRetryPreparesEnvironment(t *testing.T) {
	s := storageRecoveryServer(t)
	s.failStorageRecovery(errors.New("previous attempt lost its helper connection"))
	response := desktopRequestForTest(s, http.MethodPost, "/api/environment/storage/recover", []byte(`{}`))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	job := awaitJobIdle(t, &s.jobs)
	if job.Error != "" || job.Kind != "storage-recovery" || s.storageRecoveryStatus().Required || s.ready {
		t.Fatal("successful retry failed to restore admission without starting a VM", job, s.storageRecoveryStatus())
	}
	if info, err := os.Stat(s.environmentDir()); err != nil || !info.IsDir() {
		t.Fatal("normal environment preparation did not run", err)
	}
	if response := desktopRequestForTest(s, http.MethodGet, "/api/environment/storage", nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"directory"`) {
		t.Fatal("recovered storage form remained unavailable", response.Code, response.Body.String())
	}
	admitted := false
	if _, err := s.submitJob("start", func() error { admitted = true; return nil }); err != nil {
		t.Fatal("successful recovery kept normal start admission closed", err)
	}
	awaitJobIdle(t, &s.jobs)
	if !admitted {
		t.Fatal("normal start was not executed")
	}
}

func TestStorageRecoveryFailureAndReadOnlyLocationPreserveJournal(t *testing.T) {
	s := storageRecoveryServer(t)
	journal := storageRecoveryJournal(t, s)
	original, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	response := desktopRequestForTest(s, http.MethodGet, "/api/environment/storage", nil)
	if response.Code != http.StatusOK || !s.storageRecoveryStatus().Required {
		t.Fatal("location did not expose native recovery", response.Code, response.Body.String())
	}
	for _, repair := range []bool{false, true} {
		response = desktopRequestForTest(s, http.MethodPost, "/api/environment/storage/recover", []byte(fmt.Sprintf(`{"repair":%t}`, repair)))
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		job := awaitJobIdle(t, &s.jobs)
		state := s.storageRecoveryStatus()
		if job.Error == "" || !state.Required || state.Error != job.Error || s.ready {
			t.Fatal("failed recovery cleared the cause or resumed writes", repair, job, state)
		}
		if current, err := os.ReadFile(journal); err != nil || !bytes.Equal(current, original) {
			t.Fatal("failed native recovery changed its durable ownership record", repair, err)
		}
	}
}

func TestStorageRecoveryPreparationFailureKeepsWritesClosed(t *testing.T) {
	s := storageRecoveryServer(t)
	if err := os.WriteFile(s.environmentDir(), []byte("synthetic occupied workspace path"), 0600); err != nil {
		t.Fatal(err)
	}
	s.failStorageRecovery(errors.New("previous native recovery failure"))
	response := desktopRequestForTest(s, http.MethodPost, "/api/environment/storage/recover", []byte(`{}`))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	job := awaitJobIdle(t, &s.jobs)
	if job.Error == "" || !s.storageRecoveryStatus().Required || s.ready {
		t.Fatal("failed workspace preparation resumed environment writes", job)
	}
	if response := desktopRequestForTest(s, http.MethodPost, "/api/start", []byte(`{}`)); response.Code != http.StatusConflict {
		t.Fatal("preparation failure opened start admission", response.Code)
	}
}

func TestStorageRecoveryKeepsAuthentication(t *testing.T) {
	s := storageRecoveryServer(t)
	s.failStorageRecovery(errors.New("synthetic pending migration"))
	if response := request(s, http.MethodPost, "/api/environment/storage/recover", s.origin, `{}`, false); response.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated recovery was admitted", response.Code)
	}
	s.account.lock()
	if response := desktopRequestForTest(s, http.MethodPost, "/api/environment/storage/recover", []byte(`{}`)); response.Code != http.StatusLocked {
		t.Fatal("locked account could recover storage", response.Code)
	}
	s.account.setKey(make([]byte, 32))
	local := testServer(t)
	local.remote = newRemoteGateway(local.profile)
	defer local.remote.manager.Close()
	local.remote.setRemote(true)
	if response := request(local, http.MethodPost, "/api/environment/storage/recover", local.origin, `{}`, true); response.Code != http.StatusForbidden {
		t.Fatal("remote connection could dispatch local recovery", response.Code)
	}
}

func TestStorageRecoveryDoesNotCaptureUnrelatedNetworkFailure(t *testing.T) {
	s := storageRecoveryServer(t)
	s.networkIntentError = "synthetic proxy connection failure"
	response := desktopRequestForTest(s, http.MethodGet, "/api/status", nil)
	if response.Code != http.StatusOK || s.storageRecoveryStatus().Required || !strings.Contains(response.Body.String(), s.networkIntentError) {
		t.Fatal("network failure became a storage failure", response.Code, response.Body.String())
	}
}

func TestStorageRecoveryProductionStartupKeepsLoginAndDiagnostics(t *testing.T) {
	const helper = "CAGE_TEST_STORAGE_RECOVERY_CONFIG"
	if config := os.Getenv(helper); config != "" {
		if err := Serve(config); err != nil {
			t.Fatal(err)
		}
		return
	}
	s := storageRecoveryServer(t)
	journal := storageRecoveryJournal(t, s)
	original, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(s.profile, "initialization.json"), initializationRecord{Version: 1, Started: true}); err != nil {
		t.Fatal(err)
	}
	account, err := loadLocalAccount(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.create("Storage QA", "synthetic-recovery-password", ""); err != nil {
		t.Fatal(err)
	}
	account.lock()
	// The go test deadline is the only hang guard: startup and the password key
	// derivation can take any time on a loaded host. The helper's own timeout
	// only stops an orphan if this process is killed.
	ctx := t.Context()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStorageRecoveryProductionStartupKeepsLoginAndDiagnostics$", "-test.timeout=10m")
	// The production startup must not see the developer's real home or caches.
	home := t.TempDir()
	command.Env = append(os.Environ(), "CAGE_TEST_STORAGE_RECOVERY_CONFIG="+s.configPath, "HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"), "XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"), "APPDATA="+filepath.Join(home, "AppData", "Roaming"))
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if !finished {
			_ = command.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Log(output.String())
		}
	})
	var control controlFile
	// Wait for the published control record, or for the workbench to exit.
	for {
		data, err := os.ReadFile(filepath.Join(s.profile, "control.json"))
		if err == nil && json.Unmarshal(data, &control) == nil && control.PID == command.Process.Pid {
			break
		}
		select {
		case err := <-done:
			finished = true
			t.Fatalf("storage recovery terminated the production workbench: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if control.PID != command.Process.Pid {
		t.Fatal("pending recovery did not publish a reachable workbench")
	}
	origin := fmt.Sprintf("http://localhost:%d", control.Port)
	base, _ := url.Parse(origin)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(base, []*http.Cookie{{Name: cookieName + "_" + base.Port() + "-desktop", Value: trustedViewProof(control.Token), Path: "/"}})
	// Requests carry the test context; unlocking derives the password key and
	// has no fixed time limit here.
	client := &http.Client{Jar: jar}
	call := func(method, path, body string, expected int) []byte {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, origin+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != expected {
			t.Fatalf("%s %s returned %d %s (%v)", method, path, response.StatusCode, data, err)
		}
		return data
	}
	var launch launchResponse
	if err := json.Unmarshal(call(http.MethodPost, "/api/launch", fmt.Sprintf(`{"token":%q}`, control.Token), http.StatusOK), &launch); err != nil {
		t.Fatal(err)
	}
	entry, err := url.Parse(launch.URL)
	if err != nil || entry.Fragment == "" {
		t.Fatal("launcher did not issue a workbench ticket", err)
	}
	call(http.MethodPost, "/api/session", fmt.Sprintf(`{"token":%q}`, entry.Fragment), http.StatusOK)
	call(http.MethodGet, "/api/account", "", http.StatusOK)
	call(http.MethodGet, "/api/status", "", http.StatusLocked)
	call(http.MethodPost, "/api/account/unlock", `{"password":"synthetic-recovery-password"}`, http.StatusOK)
	var status struct {
		Ready    bool                `json:"ready"`
		Recovery storageRecoveryView `json:"storage_recovery"`
	}
	if err := json.Unmarshal(call(http.MethodGet, "/api/status", "", http.StatusOK), &status); err != nil || status.Ready || !status.Recovery.Required || status.Recovery.Error == "" {
		t.Fatal("startup did not retain the actual native failure", status, err)
	}
	call(http.MethodGet, "/api/settings", "", http.StatusOK)
	var initialization initializationView
	if err := json.Unmarshal(call(http.MethodGet, "/api/environment/initialization", "", http.StatusOK), &initialization); err != nil || initialization.Required {
		t.Fatal("old initialization record hid the storage recovery screen", initialization, err)
	}
	call(http.MethodPost, "/api/start", `{}`, http.StatusConflict)
	bundle := call(http.MethodGet, "/api/diagnostics/export", "", http.StatusOK)
	if _, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle))); err != nil {
		t.Fatal("live workbench did not export a valid diagnostic bundle", err)
	}
	if current, err := os.ReadFile(journal); err != nil || !bytes.Equal(current, original) {
		t.Fatal("workbench access changed the pending native transaction", err)
	}
	if _, err := os.Stat(s.environmentDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending startup wrote environment data", err)
	}
}
