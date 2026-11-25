package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
)

func initializationRequest(s *Server, action string, trusted bool) *httptest.ResponseRecorder {
	method, body := http.MethodGet, ""
	if action != "" {
		method, body = http.MethodPost, `{"action":"`+action+`"}`
	}
	r := httptest.NewRequest(method, s.origin+"/api/environment/initialization", strings.NewReader(body))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	if trusted {
		r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}

func TestInitializationBeginAndInstallAfterFirstAccountCreation(t *testing.T) {
	s := testServer(t)
	account, err := loadLocalAccount(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	s.account = account
	t.Cleanup(account.lock)

	actions := []string{"begin", "install"}
	for _, action := range actions {
		if w := initializationRequest(s, action, true); w.Code != http.StatusLocked {
			t.Fatalf("%s before account creation: %d %s", action, w.Code, w.Body.String())
		}
	}
	path := filepath.Join(s.profile, "initialization.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no initialization record before account creation, stat error: %v", err)
	}

	w := request(s, http.MethodPost, "/api/account/create", s.origin, `{"username":"firstuser","password":"test-password","confirm_password":"test-password"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("create first account: %d %s", w.Code, w.Body.String())
	}
	for _, action := range actions {
		if w := initializationRequest(s, action, true); w.Code != http.StatusOK {
			t.Fatalf("%s after account creation: %d %s", action, w.Code, w.Body.String())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record initializationRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		want := initializationRecord{Version: 1, Started: action == "install"}
		if record != want {
			t.Fatalf("%s wrote %+v, want %+v", action, record, want)
		}
	}
}

func TestInitializationPersistsBeforeDiskCreationAndDoesNotResetExisting(t *testing.T) {
	s := testServer(t)
	read := func() initializationView {
		t.Helper()
		w := initializationRequest(s, "", true)
		var view initializationView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil {
			t.Fatalf("initialization: %d %s", w.Code, w.Body.String())
		}
		return view
	}
	if !read().Required {
		t.Fatal("fresh profile bypassed initialization")
	}
	for _, action := range []string{"begin", "install"} {
		if w := initializationRequest(s, action, true); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if err := os.MkdirAll(filepath.Join(s.profile, "state", "lima", "base"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.profile, "state", "lima", "base", "lima.yaml"), []byte("vmType: qemu\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.profile, "state", "lima", "base", "diffdisk"), []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.profile, "state", ".agent-vm-base-version"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	view := read()
	if !view.Required || !view.Started || !view.Tracked {
		t.Fatalf("partial installation bypassed durable state: %+v", view)
	}
	// A new controller sees the same state, even after creating its base disk.
	restarted := testServer(t)
	restarted.profile = s.profile
	if next, err := restarted.initializationState(); err != nil || !next.Required || !next.Started {
		t.Fatalf("restart: %+v %v", next, err)
	}
	if err := os.Remove(filepath.Join(s.profile, "initialization.json")); err != nil {
		t.Fatal(err)
	}
	if read().Required {
		t.Fatal("existing Linux installation forced to reinstall")
	}
}

func TestInitializationCompletionRequiresReadyGuestAndCurrentNetworkEvidence(t *testing.T) {
	s := testServer(t)
	if w := initializationRequest(s, "begin", false); w.Code != http.StatusForbidden {
		t.Fatal("untrusted initialization accepted", w.Code)
	}
	if w := initializationRequest(s, "install", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := initializationRequest(s, "complete", true); w.Code == 200 {
		t.Fatal("unready guest accepted")
	}
	marker := filepath.Join(s.instanceDir(), "cage-running.json")
	if err := writePrivate(marker, map[string]int{"pid": os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	s.ready = true
	if w := initializationRequest(s, "complete", true); w.Code == 200 {
		t.Fatal("missing network report accepted")
	}
	cfg, err := cage.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	report := cage.SafetyReport{Policy: cage.SafetyPolicyVersion, ID: "initialization", RunID: "test-vm", CheckID: "test-check", Configuration: cfg.Fingerprint(), CreatedAt: time.Now()}
	for _, id := range []string{"egress_policy", "proxy_transport", "dns_transport", "guest_settings", "browser_settings", "filesystem_boundary", "management_boundary", "fail_closed", "udp_policy", "ipv6_policy", "exit_consistency"} {
		report.Checks = append(report.Checks, cage.SafetyCheck{ID: id, Critical: true, State: cage.CheckPassed, Evidence: "isolated test", CheckedAt: report.CreatedAt})
	}
	s.safety.Gate = &cage.GateStatus{Mode: cage.GateOpen, PolicyVerified: true, RunID: report.RunID, Configuration: cfg.Fingerprint(), Until: time.Now().Add(time.Minute), Report: &report}
	s.setJobForTest(Job{Running: true, Kind: "start"})
	if w := initializationRequest(s, "complete", true); w.Code == 200 {
		t.Fatal("unfinished job accepted")
	}
	s.setJobForTest(Job{})
	s.safety.Gate.Configuration = "another-config"
	if w := initializationRequest(s, "complete", true); w.Code == 200 {
		t.Fatal("stale configuration accepted")
	}
	s.safety.Gate.Configuration = cfg.Fingerprint()
	// A concurrent health read must not make the final button fail. It also
	// must not bypass the readiness/report checks above once the lock is free.
	s.op.Lock()
	time.AfterFunc(50*time.Millisecond, s.op.Unlock)
	if w := initializationRequest(s, "complete", true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.ready = false
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if view, err := s.initializationState(); err != nil || view.Required {
		t.Fatalf("completed stopped environment lost initialization: %+v %v", view, err)
	}
}

func TestEnvironmentPreparedRejectsStaleBaseMarkerWithoutVMFiles(t *testing.T) {
	s := testServer(t)
	stateDir := filepath.Join(s.profile, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Stale marker without base or instance VM files must not report prepared.
	if err := os.WriteFile(filepath.Join(stateDir, ".agent-vm-base-version"), []byte("100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.environmentPrepared() {
		t.Fatal("stale .agent-vm-base-version alone reported environmentPrepared=true")
	}
	if view, err := s.initializationState(); err != nil || !view.Required {
		t.Fatalf("stale marker without VM files skipped required setup: %+v %v", view, err)
	}

	// Base lima.yaml without a disk is still incomplete.
	baseDir := filepath.Join(stateDir, "lima", "base")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "lima.yaml"), []byte("vmType: qemu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.environmentPrepared() {
		t.Fatal("base lima.yaml without disk reported environmentPrepared=true")
	}

	// Adding base diffdisk makes the base template prepared.
	if err := os.WriteFile(filepath.Join(baseDir, "diffdisk"), []byte("qcow2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.environmentPrepared() {
		t.Fatal("intact base template reported environmentPrepared=false")
	}

	// An intact active instance remains prepared even if base + marker are absent.
	if err := os.RemoveAll(baseDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stateDir, ".agent-vm-base-version")); err != nil {
		t.Fatal(err)
	}
	if s.environmentPrepared() {
		t.Fatal("removed base without instance reported environmentPrepared=true")
	}
	instDir := s.instanceDir()
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "lima.yaml"), []byte("vmType: qemu\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "basedisk"), []byte("raw"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.environmentPrepared() {
		t.Fatal("intact active instance reported environmentPrepared=false")
	}
}
