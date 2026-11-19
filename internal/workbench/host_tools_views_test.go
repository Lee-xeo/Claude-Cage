package workbench

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

// A plugin's page is served only under its own secret path, only from
// inside the plugin, and with headers that leave it nothing but its own
// files to load.
func TestPluginViewFilesAreServedConfined(t *testing.T) {
	s := &Server{profile: t.TempDir(), token: "test-token-test-token-test-token-0123", origin: "http://localhost:1234"}
	dir := filepath.Join(s.hostPluginDir(), "notes")
	write := func(name, content string) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(hostbridge.ManifestFile, `{"name":"notes","version":"1","description":"A plugin.","permissions":[],"engines":{"cage":">=0.1.0"},"kind":"host",
		"contributes":{"views":[{"id":"main","title":"Notes","entry":"view/index.html"}]}}`)
	write("view/index.html", "<p>page</p>")
	write("view/app.js", "1")
	write("secret.txt", "outside the page's folder")
	if err := os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(dir, "view", "link.js")); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		s.servePluginView(recorder, httptest.NewRequest("GET", path, nil))
		return recorder
	}
	base := s.hostViewURL("notes", "main")
	if !strings.HasPrefix(base, "/plugin-views/notes/") || !strings.HasSuffix(base, "/main/") {
		t.Fatalf("address %s", base)
	}
	page := get(base)
	policy := page.Header().Get("Content-Security-Policy")
	if page.Code != 200 || page.Body.String() != "<p>page</p>" || !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %q %q", page.Code, page.Body, page.Header().Get("Content-Type"))
	}
	for _, want := range []string{"default-src 'none'", "connect-src 'none'", "script-src http://localhost:1234/plugin-views/notes/;", "frame-ancestors http://localhost:1234", "form-action 'none'", "base-uri 'none'", "sandbox allow-scripts"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy lacks %q: %s", want, policy)
		}
	}
	if page.Header().Get("X-Frame-Options") != "" || page.Header().Get("X-Content-Type-Options") != "nosniff" || page.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", page.Header())
	}
	if page.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("fonts and module scripts could not be fetched by the page")
	}
	if script := get(base + "app.js"); script.Code != 200 || script.Body.String() != "1" || script.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("script: %d", script.Code)
	}
	for name, path := range map[string]string{
		"another key":        strings.Replace(base, s.hostViewKey("notes"), strings.Repeat("0", 32), 1),
		"a link out":         base + "link.js",
		"above the folder":   base + "../secret.txt",
		"encoded above":      base + "..%2fsecret.txt",
		"the manifest":       base + "../plugin.json",
		"an unknown page":    strings.Replace(base, "/main/", "/other/", 1),
		"an unknown plugin":  s.hostViewURL("missing", "main"),
		"a folder":           base + "sub/",
		"without the folder": strings.TrimSuffix(base, "/"),
	} {
		if response := get(path); response.Code != 404 {
			t.Errorf("%s: %d %q", name, response.Code, response.Body)
		}
	}
	if post := httptest.NewRecorder(); true {
		s.servePluginView(post, httptest.NewRequest("POST", base, nil))
		if post.Code != 404 {
			t.Errorf("POST: %d", post.Code)
		}
	}
	// The desktop window's own process, with a folder of its own, serves the
	// same address: it finds the plugins and the key beside the
	// environment's configuration.
	entry := &Server{profile: t.TempDir(), token: "another-token-another-token-another-0", origin: "http://localhost:4321",
		desktopEntry: true, appUpdateConfigPath: filepath.Join(s.profile, "config.json")}
	shown := httptest.NewRecorder()
	entry.servePluginView(shown, httptest.NewRequest("GET", base, nil))
	if shown.Code != 200 || !strings.Contains(shown.Header().Get("Content-Security-Policy"), "frame-ancestors http://localhost:4321") {
		t.Fatalf("the desktop window's process: %d %s", shown.Code, shown.Header().Get("Content-Security-Policy"))
	}
	if info, err := os.Stat(filepath.Join(s.profile, "plugin-views.key")); err != nil || info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("key file: %v %v", info, err)
	}
	// Turned off, the plugin's pages are gone and no address is given out.
	s.preferences.HostPluginsOff = "notes"
	if response := get(base); response.Code != http.StatusNotFound {
		t.Errorf("a plugin turned off still served its page: %d", response.Code)
	}
	for _, plugin := range s.hostToolsView().Plugins {
		if plugin.Name == "notes" && (plugin.Contributes == nil || plugin.Contributes.Views[0].URL != "") {
			t.Errorf("address given for a plugin turned off: %+v", plugin.Contributes)
		}
	}
	s.preferences.HostPluginsOff = ""
	// A plugin on this computer has nothing behind its page while the tools
	// are turned off.
	for _, plugin := range s.hostToolsView().Plugins {
		if plugin.Name == "notes" && plugin.Contributes.Views[0].URL != "" {
			t.Errorf("address given with the tools turned off: %q", plugin.Contributes.Views[0].URL)
		}
	}
	s.preferences.HostTools = true
	for _, plugin := range s.hostToolsView().Plugins {
		if plugin.Name == "notes" && plugin.Contributes.Views[0].URL != base {
			t.Errorf("address %q, want %q", plugin.Contributes.Views[0].URL, base)
		}
	}
}

// A page's call needs a page of an enabled plugin, a server behind it, and
// a plugin that is running.
func TestPluginViewCallsAreChecked(t *testing.T) {
	s := &Server{profile: t.TempDir(), token: "test-token-test-token-test-token-0123", origin: "http://localhost:1234"}
	dir := filepath.Join(s.hostPluginDir(), "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"notes","version":"1","description":"A plugin.","permissions":[],"engines":{"cage":">=0.1.0"},"kind":"guest",
		"contributes":{"mcpServers":[{"id":"main","command":["x"]}],
		"views":[{"id":"bound","title":"A","entry":"a.html","server":"main"},{"id":"static","title":"B","entry":"b.html"}]}}`
	if err := os.WriteFile(filepath.Join(dir, hostbridge.ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	call := func(body string) (int, string) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/host-tools/call", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		s.callHostTool(recorder, request)
		return recorder.Code, recorder.Body.String()
	}
	for name, c := range map[string]struct {
		body string
		code string
	}{
		"another plugin's page": {`{"plugin":"other","view":"bound","method":"tools/list"}`, "-32002"},
		"an unknown page":       {`{"plugin":"notes","view":"none","method":"tools/list"}`, "-32002"},
		"a page with no server": {`{"plugin":"notes","view":"static","method":"tools/list"}`, "-32003"},
		"a plugin not running":  {`{"plugin":"notes","view":"bound","method":"tools/call","name":"x"}`, "-32003"},
	} {
		if status, body := call(c.body); status < 400 || !strings.Contains(body, c.code) {
			t.Errorf("%s: %d %s", name, status, body)
		}
	}
}
