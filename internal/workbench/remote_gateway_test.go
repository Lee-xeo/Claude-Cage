package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteHostManagementKeepsLocalSessionAndOriginBoundary(t *testing.T) {
	s := testServer(t)
	s.remote = newRemoteGateway(s.profile)
	defer s.remote.manager.Close()
	s.account.lock()
	if got := request(s, "GET", "/api/client/hosts", "", "", false).Code; got != 401 {
		t.Fatalf("unauthenticated host list: %d", got)
	}
	if got := request(s, "GET", "/api/client/context", "", "", true).Code; got != 200 {
		t.Fatalf("locked local account prevents connection management: %d", got)
	}
	if got := request(s, "POST", "/api/client/disconnect", "https://untrusted.example", "{}", true).Code; got != 403 {
		t.Fatalf("cross-origin connection mutation: %d", got)
	}
	s.remote.setRemote(true)
	if got := request(s, "POST", "/api/start", s.origin, "{}", true).Code; got != 409 {
		t.Fatalf("disconnected remote fell back to local start: %d", got)
	}
	if job := s.jobs.snapshot(); job.ID != "" {
		t.Fatal("disconnected remote created a local job")
	}
	s.remote.setRemote(false)
	r := httptest.NewRequest("POST", s.origin+"/api/stop", strings.NewReader("{}"))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(connectionHeader, "previous-remote-connection")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("old remote request reached local runtime: %d", w.Code)
	}
}

func TestDesktopGatewayForwardsOnlySandboxedPluginSVGIcons(t *testing.T) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16"/></svg>`
	const upstreamPolicy = "default-src *"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", upstreamPolicy)
		if r.URL.Query().Get("name") == "html" {
			w.Header().Set("Content-Type", "text/html")
		} else {
			w.Header().Set("Content-Type", "image/svg+xml")
		}
		_, _ = w.Write([]byte(svg))
	}))
	t.Cleanup(upstream.Close)
	s := testServer(t)
	s.remote = newDesktopGateway(s.profile, &gatewayLocalFixture{endpoint: upstream})
	t.Cleanup(s.remote.close)
	d := s.remote.desktop
	generation := chooseLocal(t, d, d.context().ConnectionID)

	icon := gatewayRequest(s, "/api/host-tools/icon?name=system-status&connection="+generation, "")
	if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/svg+xml" || icon.Body.String() != svg {
		t.Fatalf("plugin SVG was not preserved: %d %s %s", icon.Code, icon.Header().Get("Content-Type"), icon.Body.String())
	}
	policies := strings.Join(icon.Header().Values("Content-Security-Policy"), "\n")
	if !strings.Contains(policies, "default-src 'none'; style-src 'unsafe-inline'; sandbox") || strings.Contains(policies, upstreamPolicy) ||
		icon.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("plugin SVG lost its gateway protection: %v", icon.Header())
	}

	for _, path := range []string{"/api/host-tools/icon?name=html", "/api/status"} {
		if response := gatewayRequest(s, path, generation); response.Code != http.StatusBadGateway {
			t.Errorf("unexpected active content reached the client for %s: %d", path, response.Code)
		}
	}
}

func TestRemoteIdentityPersistsAndCorruptionDoesNotReplaceIt(t *testing.T) {
	profile := t.TempDir()
	first, err := ensureRemoteInstance(profile)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ensureRemoteInstance(profile)
	if err != nil || first != again {
		t.Fatal("identity changed on reconnect", err)
	}
	other, err := ensureRemoteInstance(t.TempDir())
	if err != nil || first == other {
		t.Fatal("different instances share an identity", err)
	}
	path := filepath.Join(profile, "remote-instance.json")
	corrupt := []byte(`{"id":"invalid"}`)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureRemoteInstance(profile); err == nil {
		t.Fatal("corrupt identity was accepted")
	}
	retained, _ := os.ReadFile(path)
	if string(retained) != string(corrupt) {
		t.Fatal("corrupt identity was silently replaced")
	}
}

func TestRemoteDirectoryPickerHandlesEmptyProjectsAndBlocksPrivateSymlinks(t *testing.T) {
	s := testServer(t)
	empty := t.TempDir()
	w := request(s, "GET", "/api/projects/directories?path="+url.QueryEscape(empty), "", "", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Directories []any `json:"directories"`
		Selectable  bool  `json:"selectable"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Directories) != 0 || !result.Selectable {
		t.Fatal("empty project unavailable", w.Body.String())
	}
	link := filepath.Join(t.TempDir(), "private-link")
	paths := []string{s.profile}
	if err := os.Symlink(s.profile, link); err == nil {
		paths = append(paths, link)
	}
	for _, path := range paths {
		w := request(s, "GET", "/api/projects/directories?path="+url.QueryEscape(path), "", "", true)
		if w.Code != 403 {
			t.Fatalf("private directory was listed: %d", w.Code)
		}
	}
}

func TestRemoteDownloadsAreScopedToPersistentInstance(t *testing.T) {
	profile := t.TempDir()
	first := ClientContext{Mode: "remote", InstanceID: strings.Repeat("a", 64)}
	second := ClientContext{Mode: "remote", InstanceID: strings.Repeat("b", 64)}
	one, err := clientDownloadProfile(profile, first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := clientDownloadProfile(profile, second)
	if err != nil {
		t.Fatal(err)
	}
	saved := SavedDownload{ID: testBrowserFileID, InstanceID: first.InstanceID, Name: "project.zip", Path: filepath.Join(t.TempDir(), "project.zip")}
	if err := rememberDownload(one, saved, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readSavedDownload(two, saved.ID); !os.IsNotExist(err) {
		t.Fatal("another host inherited the local file", err)
	}
	if _, err := clientDownloadProfile(profile, ClientContext{Mode: "remote", InstanceID: "../../private"}); err == nil {
		t.Fatal("invalid instance path accepted")
	}
}
