package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

func TestMacOSConnectionIgnoresSSHConfigOverrides(t *testing.T) {
	s := testServer(t)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "devuser"}
	if err := os.MkdirAll(s.instanceDir(), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := "Host lima-mac\n  User root\n  HostName 192.0.2.1\n  Port 54432\n  ProxyCommand false\n  StrictHostKeyChecking no\n  ForwardAgent yes\n"
	if err := os.WriteFile(filepath.Join(s.instanceDir(), "ssh.config"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := s.sshCommand(context.Background(), "-tt", s.sshTarget(), "--", "uname -s")
	for _, value := range []string{os.DevNull, "54432", "devuser", "Hostname=127.0.0.1", "HostKeyAlias=cage-macos", "StrictHostKeyChecking=yes", "ForwardAgent=no", "ClearAllForwardings=yes", "cage-macos"} {
		if !slices.Contains(cmd.Args, value) {
			t.Errorf("bound SSH missing %q", value)
		}
	}
	for _, value := range []string{"192.0.2.1", "root", "ForwardAgent=yes", "StrictHostKeyChecking=no", "lima-mac"} {
		if slices.Contains(cmd.Args, value) {
			t.Errorf("untrusted override %q used", value)
		}
	}
	if !slices.Contains(cmd.Args, "UserKnownHostsFile="+strconv.Quote(filepath.Join(s.profile, "macos-known-hosts"))) {
		t.Fatal("missing profile-scoped host key")
	}
	for _, bad := range []string{"", "Port 0", "Port 65536", "Port 22\nPort 23", "Port 22 -o Anything"} {
		if _, err := guestos.MacOSSSHPort([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestMacOSWorkbenchLocalViewIsolation(t *testing.T) {
	s := testServer(t)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "guestdev"}
	h := s.handler()
	check := func(path string, trusted bool, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", s.origin+path, nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
		if trusted {
			r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s trusted=%v: got %d; want %d", path, trusted, w.Code, want)
		}
		if path == "/api/account" && trusted && !strings.Contains(w.Body.String(), `"guest_system":"macos"`) {
			t.Fatal("trusted view did not select the macOS workbench")
		}
	}
	check("/api/account", false, http.StatusForbidden)
	check("/api/account", true, http.StatusOK)
	r := httptest.NewRequest("GET", s.origin+"/api/account", nil)
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var account accountView
	if err := json.Unmarshal(w.Body.Bytes(), &account); err != nil || account.MacOSGuestUser != "guestdev" {
		t.Fatalf("actual guest login was not returned: %s", w.Body.String())
	}

	check("/api/browser/tabs", true, http.StatusConflict)
	check("/api/proxy", false, http.StatusForbidden)
	check("/api/mobile/pair", true, http.StatusConflict)
}

func TestMacOSReadyBrowserRoutesUseNativeGuest(t *testing.T) {
	s := testServer(t)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "guestdev"}
	s.ready = true
	s.windowsBrowserControl = fixtureWindowsBrowserClient(t)
	r := windowsBrowserRequestForTest(s, http.MethodGet, "/api/browser/tabs", nil)
	if r.Code != http.StatusOK || !json.Valid(r.Body.Bytes()) || s.browserControl != nil {
		t.Fatalf("ready Mac browser failed to use the native transport: %d", r.Code)
	}
	if !macOSWorkbenchRoute("/api/input/files/browser/chunks") || macOSWorkbenchRoute("/api/input/files/desktop/chunks") {
		t.Fatal("native browser upload route boundary mismatch")
	}
}

func TestMacOSProjectRejectsSiblingProfileAndCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	profile := filepath.Join(home, ".claude-cage", "macos-trial")
	dailyState := filepath.Join(home, ".claude-cage", "state", "lima")
	credentials := filepath.Join(home, ".ssh")
	project := filepath.Join(home, "Cage Projects", "demo")
	for _, dir := range []string{profile, dailyState, credentials, project} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{profile: profile, macosGuest: &guestos.MacOSProfile{User: "guestdev"}}
	for _, dir := range []string{dailyState, credentials} {
		if err := s.ensureProjectShare(dir); err == nil || !strings.Contains(err.Error(), "共享") {
			t.Fatalf("%s was not rejected before contacting the guest: %v", dir, err)
		}
	}
	if err := validateHostProjectData(project, profile); err != nil {
		t.Fatal(err)
	}
}

func TestMacOSProjectApprovalIsBoundToOnePendingShare(t *testing.T) {
	s := testServer(t)
	s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "guestdev"}
	s.ready = true
	folder := t.TempDir()
	share := &projectShare{}
	s.projectShares = map[string]*projectShare{folder: share}
	s.beginMacOSProjectApproval(folder, share)
	grant := share.approval
	t.Cleanup(func() { share.approvalTimer.Stop() })
	call := func(path, proof string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"workspace": path, "grant": proof, "action": "cancel"})
		r := httptest.NewRequest("POST", s.origin+"/api/macos/project-approval", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.origin)
		r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
		r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		return w.Code
	}
	if call(folder, "invalid") != http.StatusConflict || call(folder+"/other", grant) != http.StatusConflict || s.projectShares[folder] != share {
		t.Fatal("unrelated approval canceled the pending host share")
	}
	if call(folder, grant) != 200 || s.projectShares[folder] != nil || call(folder, grant) != http.StatusConflict {
		t.Fatal("a canceled project grant can still be reused")
	}
}

func TestInitialMacSetupQuarantinesRegularWorkbench(t *testing.T) {
	s := testServer(t)
	s.macosGuest = &guestos.MacOSProfile{User: "cageuser", Instance: "mac", SetupID: "new"}
	// An unreadable binding is also quarantined. Allow only UI bootstrap,
	// account authentication, and the startup/first-setup endpoints.
	for _, path := range []string{"/api/browser/tabs", "/api/terminals", "/api/macos/login", "/api/projects/open", "/api/safety/check", "/api/macos/claude/window"} {
		w := request(s, "GET", path, s.origin, "", true)
		if w.Code != http.StatusConflict {
			t.Errorf("setup exposed %s: %d", path, w.Code)
		}
	}
	s.macosGuest.SetupID = ""
	for _, action := range []string{"open", "finish", "frame", "fill-password"} {
		req := httptest.NewRequest("POST", s.origin+"/api/macos/first-setup", strings.NewReader(`{"action":"`+action+`"}`))
		req.Header.Set("Origin", s.origin)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
		req.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, req)
		if w.Code != http.StatusGone {
			t.Errorf("completed setup allowed %s: %d", action, w.Code)
		}
	}
}

func TestPendingMacOSInstallQuarantinesWorkbench(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the recovery record's profile directory sync is denied on Windows")
	}
	s := testServer(t)
	id := uuid.New()
	if err := guestos.SaveInstallation(s.profile, guestos.Installation{Version: 1, System: guestos.MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/api/terminals", "/api/browser/tabs", "/api/projects/open", "/api/safety/check", "/api/macos/maintenance", "/api/macos/first-setup"}
	for _, path := range paths {
		if response := request(s, http.MethodGet, path, s.origin, "", true); response.Code != http.StatusConflict {
			t.Errorf("pending Mac exposed %s: %d", path, response.Code)
		}
	}
	prepare := httptest.NewRequest(http.MethodPost, s.origin+"/api/macos/install-recovery", strings.NewReader(`{"action":"prepare"}`))
	prepare.Header.Set("Origin", s.origin)
	prepare.Header.Set("Content-Type", "application/json")
	prepare.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	prepare.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	prepared := httptest.NewRecorder()
	s.handler().ServeHTTP(prepared, prepare)
	if prepared.Code != http.StatusConflict {
		t.Fatalf("missing guest identity was guessed: %d %s", prepared.Code, prepared.Body.String())
	}
	machine := filepath.Join(s.profile, "state", "lima", "mac")
	if err := os.MkdirAll(machine, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "vz-identifier"), []byte("failed-install-machine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "disk"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	pending, err := guestos.LoadInstallation(s.profile)
	if err != nil || pending == nil || pending.ID != id {
		t.Fatalf("pending installation missing after failure: %+v %v", pending, err)
	}
	if err := guestos.CreateMacOSInstallRecovery(s.profile, "cageuser", id); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if response := request(s, http.MethodGet, path, s.origin, "", true); response.Code != http.StatusConflict {
			t.Errorf("recovery identity exposed %s: %d", path, response.Code)
		}
	}
	get := httptest.NewRequest(http.MethodGet, s.origin+"/api/macos/install-recovery", nil)
	get.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	get.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	response := httptest.NewRecorder()
	s.handler().ServeHTTP(response, get)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"available":true`) {
		t.Fatalf("pending failure did not expose recovery entry: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(machine); err != nil {
		t.Fatalf("pending guest disk was lost: %v", err)
	}
}

func TestMacOSSetupAdministratorIdentity(t *testing.T) {
	for _, user := range []string{"cage", "cageprovision"} {
		t.Run(user, func(t *testing.T) {
			s := testServer(t)
			s.macosGuest = &guestos.MacOSProfile{Instance: "mac", User: "cage"}
			if err := os.MkdirAll(s.instanceDir(), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.instanceDir(), "ssh.config"), []byte("User "+user+"\nPort 12345\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := s.macOSSetupAdminCommand(t.Context())
			if cmd.Err != nil {
				t.Fatal(cmd.Err)
			}
			if !slices.Contains(cmd.Args, user) || cmd.Args[len(cmd.Args)-1] != "exec /usr/bin/sudo -S -p '' /usr/local/libexec/cage-finish-initial-setup < /Users/"+user+"/password" {
				t.Fatalf("incorrect setup identity: %v", cmd.Args)
			}
		})
	}
}

func TestMacOSComponentDriftFromReceipt(t *testing.T) {
	required := cage.GuestManifest{Protocol: 1, Revision: "7edc041c", AppVersion: "0.1.7"}
	for name, test := range map[string]struct {
		receipt string
		drifted bool
	}{
		"current":            {`{"protocol":1,"revision":"7edc041c","app_version":"0.1.7","system":"macos"}`, false},
		"older app version":  {`{"protocol":1,"revision":"7edc041c","app_version":"0.1.7-rc.1","system":"macos"}`, false},
		"older revision":     {`{"protocol":1,"revision":"dd93682c","app_version":"0.1.7-rc.1","system":"macos"}`, true},
		"other protocol":     {`{"protocol":2,"revision":"7edc041c","system":"macos"}`, true},
		"other system":       {`{"protocol":1,"revision":"7edc041c","system":"windows11"}`, true},
		"missing receipt":    {`null`, true},
		"unreadable receipt": {`not json`, true},
	} {
		if got := macOSComponentsDrifted([]byte(test.receipt), required); got != test.drifted {
			t.Errorf("%s: drifted=%v, want %v", name, got, test.drifted)
		}
	}
}
