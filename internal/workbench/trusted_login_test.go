package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTrustedLocalLifecycle(t *testing.T) {
	profile, devices := t.TempDir(), t.TempDir()
	a, err := loadLocalAccount(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("create", "", devices); err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(a.key)
	a.lock()
	a, err = loadLocalAccount(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("resume", "", t.TempDir()); err == nil {
		t.Fatal("untrusted OS profile opened workspace")
	}
	if err = a.trustedLogin("resume", "", devices); err != nil || !bytes.Equal(original, a.key) {
		t.Fatal("restart lost workspace", err)
	}
	if err = a.trustedLogin("protect", "123", devices); err == nil {
		t.Fatal("accepted short password")
	}
	if err = a.trustedLogin("protect", "first-password", devices); err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("create", "", devices); err == nil {
		t.Fatal("overwrote password account")
	}
	a.lock()
	if err = a.trustedLogin("resume", "", devices); err == nil {
		t.Fatal("stale device grant")
	}
	if err = a.unlock("first-password"); err != nil || !bytes.Equal(original, a.key) {
		t.Fatal("password conversion lost workspace", err)
	}
	if err = a.trustedLogin("enable", "first-password", devices); err != nil {
		t.Fatal(err)
	}
	a.lock()
	if err = a.trustedLogin("resume", "", devices); err != nil || !bytes.Equal(original, a.key) {
		t.Fatal("password account failed automatic unlock", err)
	}
	if err = a.lockAccess(false); err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("resume", "", devices); err == nil {
		t.Fatal("ignored explicit lock")
	}
	if err = a.unlock("first-password"); err != nil {
		t.Fatal(err)
	}
	if err = a.lockAccess(true); err != nil {
		t.Fatal(err)
	}
	a, err = loadLocalAccount(profile)
	if err != nil {
		t.Fatal(err)
	}
	if a.view().TrustedLogin || a.trustedLogin("resume", "", devices) == nil {
		t.Fatal("logout retained trust")
	}
}

func TestTrustedLocalLauncherBoundary(t *testing.T) {
	s := testServer(t)
	if w := request(s, "POST", "/api/desktop-local/trusted-login", s.origin, `{"action":"resume"}`, true); w.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary session reached launcher action: %d", w.Code)
	}
	if w := request(s, "POST", "/api/account/trusted-login", s.origin, `{}`, true); w.Code != http.StatusNotFound {
		t.Fatalf("ordinary account route accepted trusted action: %d", w.Code)
	}
}

func TestTrustedPasswordRememberMigrationAndRepair(t *testing.T) {
	profile, devices := t.TempDir(), t.TempDir()
	a, err := loadLocalAccount(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.create("Remember test", "first-password", ""); err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(a.key)
	automatic, err := a.enrollDevice("automatic")
	if err != nil {
		t.Fatal(err)
	}
	biometric, err := a.enrollDevice("biometric")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("enable", "first-password", devices); err != nil {
		t.Fatal(err)
	}
	if a.record.Devices["automatic"].ID != "" || a.record.Devices["biometric"].ID != biometric.ID {
		t.Fatal("migration did not replace only the automatic grant")
	}
	firstID := a.record.TrustedUnlock.ID
	// Copying a password account to a new OS profile must require the password.
	a.lock()
	a, err = loadLocalAccount(profile)
	if err != nil {
		t.Fatal(err)
	}
	otherDevice := t.TempDir()
	if err = a.trustedLogin("resume", "", otherDevice); err == nil {
		t.Fatal("copied account unlocked on a different device")
	}
	if err = a.unlock("first-password"); err != nil {
		t.Fatal(err)
	}
	// Remember me must replace a stale record even if TrustedUnlock is set.
	if err = a.trustedLogin("enable", "first-password", otherDevice); err != nil {
		t.Fatal(err)
	}
	if a.record.TrustedUnlock.ID == firstID {
		t.Fatal("stale grant was not replaced")
	}
	previousID := a.record.TrustedUnlock.ID
	if err = os.Remove(filepath.Join(otherDevice, previousID+".json")); err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("enable", "first-password", otherDevice); err != nil {
		t.Fatal(err)
	}
	a.lock()
	a, err = loadLocalAccount(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("resume", "", otherDevice); err != nil || !bytes.Equal(original, a.key) {
		t.Fatal("repaired grant did not survive restart with the original workspace key", err)
	}
	// Disabling remember must revoke a legacy automatic grant too, including
	// when no shared trusted grant exists. Touch ID remains available.
	for range 2 {
		if _, err = a.enrollDevice("automatic"); err != nil {
			t.Fatal(err)
		}
		if err = a.trustedLogin("disable", "", otherDevice); err != nil {
			t.Fatal(err)
		}
		if a.view().TrustedLogin || a.record.Devices["automatic"].ID != "" {
			t.Fatal("opt-out left an automatic login path enabled")
		}
	}
	a.lock()
	if err = a.trustedLogin("resume", "", otherDevice); err == nil {
		t.Fatal("disabled trust reopened the account")
	}
	if err = a.unlockDevice("automatic", automatic); err == nil {
		t.Fatal("revoked legacy grant reopened the account")
	}
	if err = a.unlockDevice("biometric", biometric); err != nil || !bytes.Equal(original, a.key) {
		t.Fatal("remember opt-out broke biometric unlock or workspace data", err)
	}
}

func TestTrustedLocalUnlockIsPrivateToDesktop(t *testing.T) {
	s := testServer(t)
	a, err := loadLocalAccount(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	s.account = a
	if err = a.trustedLogin("create", "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !a.viewFor(true).Unlocked || a.viewFor(false).Unlocked {
		t.Fatal("trusted login escaped desktop view")
	}
	if _, allowed := a.accessFor(false, true); allowed {
		t.Fatal("legacy passwordless workspace granted phone access")
	}
	if response := request(s, "GET", "/api/favorites", "", "", true); response.Code != http.StatusLocked {
		t.Fatalf("session cookie alone opened favorites: %d", response.Code)
	}
	if response := request(s, "POST", "/api/account/profile", s.origin, `{"username":"Different"}`, true); response.Code != http.StatusLocked {
		t.Fatalf("session cookie alone changed profile: %d", response.Code)
	}
	req, _ := http.NewRequest("GET", s.origin, nil)
	req.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	if !s.hasTrustedView(req) {
		t.Fatal("local launcher proof was refused")
	}
}

func TestTrustedLocalDesktopEntryAndWorkload(t *testing.T) {
	for _, env := range []string{"LOCALAPPDATA", "XDG_CONFIG_HOME", "HOME"} {
		t.Setenv(env, t.TempDir())
	}
	workload := testServer(t)
	var err error
	workload.account, err = loadLocalAccount(workload.profile)
	if err != nil {
		t.Fatal(err)
	}
	workload.instanceID = randomToken()
	server := httptest.NewUnstartedServer(workload.handler())
	port := server.Listener.Addr().(*net.TCPAddr).Port
	workload.origin = "http://localhost:" + strconv.Itoa(port)
	server.Start()
	defer server.Close()
	record := controlFile{Port: port, PID: os.Getpid(), Token: workload.token}
	if err = writePrivate(filepath.Join(workload.profile, "control.json"), record); err != nil {
		t.Fatal(err)
	}
	c, err := openController(workload.profile)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	life := t.Context()
	bootstrap, cancel := context.WithTimeout(life, 5*time.Second)
	defer cancel()
	local := &desktopLocal{profile: workload.profile}
	session, err := local.localSession(life, bootstrap, c, desktopLocalReply{Identity: workload.identity, InstanceID: workload.instanceID})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	gateway := testServer(t)
	gateway.remote = newDesktopGateway(gateway.profile, local)
	d := gateway.remote.desktop
	d.active = &desktopTarget{id: d.generation, mode: "local", ctx: life, session: session}
	body, _ := json.Marshal(trustedLoginRequest{Action: "create", ConnectionID: d.generation})
	w := request(gateway, "POST", "/api/client/trusted-login", gateway.origin, string(body), true)
	if w.Code != http.StatusConflict || workload.account.view().Exists {
		t.Fatalf("fresh profile bypassed registration: %d %s", w.Code, w.Body.String())
	}
	// An existing trusted workspace still resumes after upgrading.
	base, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		base = os.Getenv("LOCALAPPDATA")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = workload.account.trustedLogin("create", "", filepath.Join(base, "Cage", "trusted-local")); err != nil {
		t.Fatal(err)
	}
	read := gatewayRequest(gateway, "/api/account", d.generation)
	var view accountView
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &view) != nil || !view.Unlocked || !view.TrustedLocal {
		t.Fatalf("local gateway cannot see unlocked account: %s", read.Body.String())
	}
	workload.account.lock()
	body, _ = json.Marshal(trustedLoginRequest{Action: "resume", ConnectionID: d.generation})
	w = request(gateway, "POST", "/api/client/trusted-login", gateway.origin, string(body), true)
	if w.Code != http.StatusOK || !workload.account.view().Unlocked {
		t.Fatalf("local resume failed: %d %s", w.Code, w.Body.String())
	}
	// Existing password accounts use the same launcher path on every platform.
	for _, action := range []string{"protect", "enable"} {
		body, _ = json.Marshal(trustedLoginRequest{Action: action, Password: "first-password", ConnectionID: d.generation})
		w = request(gateway, "POST", "/api/client/trusted-login", gateway.origin, string(body), true)
		if w.Code != http.StatusOK {
			t.Fatalf("password account %s: %d %s", action, w.Code, w.Body.String())
		}
	}
	workload.account.lock()
	workload.account, err = loadLocalAccount(workload.profile)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(trustedLoginRequest{Action: "resume", ConnectionID: d.generation})
	w = request(gateway, "POST", "/api/client/trusted-login", gateway.origin, string(body), true)
	if w.Code != http.StatusOK || !workload.account.viewFor(true).Unlocked || workload.account.viewFor(false).Unlocked {
		t.Fatalf("password account did not resume privately after reload: %d %s", w.Code, w.Body.String())
	}
}

func TestLegacyPasswordlessWorkspaceReturnsActionableMobileState(t *testing.T) {
	s := testServer(t)
	a, err := loadLocalAccount(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	s.account = a
	devices := t.TempDir()
	if err = a.trustedLogin("create", "", devices); err != nil {
		t.Fatal(err)
	}
	proof := randomToken()
	s.phones.mu.Lock()
	s.phones.slots = []*phoneSlot{{
		record:    phoneRecord{ID: "phone-1", Lookup: "lookup"},
		roomProof: proof,
		roomCtx:   context.Background(),
	}}
	s.phones.mu.Unlock()

	if _, state := a.accessStateFor(false, true); state != accountAccessDesktopOnly {
		t.Fatalf("expected desktop-only state for legacy passwordless workspace, got %v", state)
	}
	if _, allowed := a.accessFor(false, true); allowed {
		t.Fatal("legacy passwordless workspace must not grant phone access")
	}

	got := phoneRequestForTest(t, s, "GET", "/api/mobile/cli/projects", "", proof)
	if got.Code != http.StatusLocked || got.Header().Get("X-Cage-Recovery") != "desktop-account-mobile-access" || !strings.Contains(got.Body.String(), "免密码本机工作区") || !strings.Contains(got.Body.String(), "设置账户密码") {
		t.Fatalf("expected actionable legacy workspace prompt, got %d header=%q body=%q", got.Code, got.Header().Get("X-Cage-Recovery"), got.Body.String())
	}

	pairStatus := desktopRequestForTest(s, "GET", "/api/mobile-pair", nil)
	if pairStatus.Code != http.StatusOK || !strings.Contains(pairStatus.Body.String(), `"mobile_access_blocked":true`) {
		t.Fatalf("expected desktop mobile-pair status to flag blocked mobile access: %d %s", pairStatus.Code, pairStatus.Body.String())
	}

	// Setting a password and enabling remember-me enables phone access on resumed sessions.
	if err = a.trustedLogin("protect", "first-password", devices); err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("enable", "first-password", devices); err != nil {
		t.Fatal(err)
	}
	a.lock()
	if err = a.trustedLogin("resume", "", devices); err != nil {
		t.Fatal(err)
	}
	if _, state := a.accessStateFor(false, true); state != accountAccessGranted {
		t.Fatalf("expected granted phone access with remember-me enabled, got %v", state)
	}

	// Turning off remember-me during a resumed trusted session switches to actionable desktop-only prompt.
	if err = a.trustedLogin("disable", "", devices); err != nil {
		t.Fatal(err)
	}
	got = phoneRequestForTest(t, s, "GET", "/api/mobile/cli/projects", "", proof)
	if got.Code != http.StatusLocked || got.Header().Get("X-Cage-Recovery") != "desktop-account-mobile-access" || !strings.Contains(got.Body.String(), "记住登录") {
		t.Fatalf("expected remember-me prompt, got %d header=%q body=%q", got.Code, got.Header().Get("X-Cage-Recovery"), got.Body.String())
	}

	// Truly locked account reports normal locked state without recovery header.
	a.lock()
	got = phoneRequestForTest(t, s, "GET", "/api/mobile/cli/projects", "", proof)
	if got.Code != http.StatusLocked || got.Header().Get("X-Cage-Recovery") != "" || !strings.Contains(got.Body.String(), "Cage 已锁定") {
		t.Fatalf("expected normal locked response, got %d header=%q body=%q", got.Code, got.Header().Get("X-Cage-Recovery"), got.Body.String())
	}
}
