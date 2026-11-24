package hostbridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const tailscaleManifest = `{
  "name": "tailscale",
  "displayName": "Tailscale",
  "version": "1.104.5-1",
  "description": "Joins a private network.",
  "icon": "icon.svg",
  "engines": { "cage": "^1.0.0" },
  "kind": "guest",
  "permissions": ["network"],
  "activationEvents": ["onStartupFinished", "onView:tailscale", "onUri:tailscale"],
  "contributes": {
    "mcpServers": [{ "id": "node", "command": ["bin/tailnet"] }],
    "views": [{
      "id": "tailscale", "title": "Tailscale", "icon": "icon.svg", "entry": "view/index.html", "server": "node",
      "openUrls": ["https://login.tailscale.com/a/*", "https://login.tailscale.com/c/*", "http://127.0.0.1:*/*"]
    }],
    "uriHandlers": [{ "scheme": "tailscale", "server": "node", "tool": "browser_return", "reveal": "tailscale" }]
  }
}`

func TestManifestSecondFormat(t *testing.T) {
	m, err := parseManifest([]byte(tailscaleManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindGuest || len(m.Contributes.Views) != 1 || m.Schemes()[0] != "tailscale" {
		t.Fatalf("manifest %+v", m)
	}
	if m.Unavailable("0.1.8", true) != NeedsNewerApplication || m.Unavailable("1.2.0", false) != GuestNotSupported || m.Unavailable("", true) != "" {
		t.Fatalf("unavailable %q %q", m.Unavailable("0.1.8", true), m.Unavailable("1.2.0", false))
	}

	// The first format reads as a host plugin with one server.
	first, err := parseManifest([]byte(`{"name":"clock","version":"1","description":"Tells the time.","permissions":[],"command":["python3","server.py"]}`))
	if err != nil || first.Kind != KindHost || first.Contributes.MCPServers[0].ID != "clock" || first.serverCommand()[0] != "python3" || first.Unavailable("0.1.8", false) != "" {
		t.Fatalf("first format %+v: %v", first, err)
	}

	// Each change to the example breaks one rule.
	broken := map[string][2]string{
		"unknown field in a view":      {`"server": "node",`, `"server": "node", "unknown": 3,`},
		"unknown field at the top":     {`"kind": "guest",`, `"kind": "guest", "main": "x",`},
		"engines missing":              {`"engines": { "cage": "^1.0.0" },`, ``},
		"engines range":                {`^1.0.0`, `~1.0.0`},
		"engines partial version":      {`^1.0.0`, `>=1.0`},
		"kind":                         {`"kind": "guest"`, `"kind": "both"`},
		"kind missing":                 {`"kind": "guest",`, ``},
		"guest with platforms":         {`"kind": "guest",`, `"kind": "guest", "platforms": ["linux"],`},
		"command beside contributes":   {`"kind": "guest",`, `"kind": "guest", "command": ["x"],`},
		"server id":                    {`{ "id": "node", "command": ["bin/tailnet"] }`, `{ "id": "Node", "command": ["bin/tailnet"] }`},
		"server id repeated":           {`{ "id": "node", "command": ["bin/tailnet"] }`, `{ "id": "node", "command": ["a"] }, { "id": "node", "command": ["b"] }`},
		"server command":               {`"command": ["bin/tailnet"]`, `"command": []`},
		"view entry outside":           {`view/index.html`, `../index.html`},
		"view entry type":              {`view/index.html`, `view/index.js`},
		"view icon":                    {`"title": "Tailscale", "icon": "icon.svg"`, `"title": "Tailscale", "icon": "icon.gif"`},
		"view title":                   {`"title": "Tailscale",`, ``},
		"view server":                  {`"entry": "view/index.html", "server": "node"`, `"entry": "view/index.html", "server": "other"`},
		"open url wildcard host":       {`https://login.tailscale.com/a/*`, `https://*.tailscale.com/a/*`},
		"open url plain http":          {`https://login.tailscale.com/a/*`, `http://login.tailscale.com/a/*`},
		"open url wildcard in path":    {`https://login.tailscale.com/a/*`, `https://login.tailscale.com/*/a`},
		"open url without path":        {`https://login.tailscale.com/a/*`, `https://login.tailscale.com`},
		"open url other scheme":        {`https://login.tailscale.com/a/*`, `file:///etc/*`},
		"open url bare host":           {`https://login.tailscale.com/a/*`, `https://intranet/*`},
		"scheme reserved":              {`"scheme": "tailscale"`, `"scheme": "https"`},
		"scheme form":                  {`"scheme": "tailscale"`, `"scheme": "Tail_scale"`},
		"handler server":               {`"scheme": "tailscale", "server": "node"`, `"scheme": "tailscale", "server": "none"`},
		"handler tool":                 {`"tool": "browser_return", `, ``},
		"handler reveal":               {`"reveal": "tailscale"`, `"reveal": "elsewhere"`},
		"activation event":             {`"onStartupFinished"`, `"onCommand:x"`},
		"activation of a missing view": {`"onView:tailscale"`, `"onView:other"`},
		"activation of a missing link": {`"onUri:tailscale"`, `"onUri:other"`},
	}
	for name, change := range broken {
		if !strings.Contains(tailscaleManifest, change[0]) {
			t.Fatalf("%s: the example has no %q", name, change[0])
		}
		if _, err := parseManifest([]byte(strings.Replace(tailscaleManifest, change[0], change[1], 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// What the first format never had stays out of it.
	for _, extra := range []string{`"kind":"guest"`, `"engines":{"cage":">=0.1.0"}`, `"activationEvents":["onStartupFinished"]`} {
		if _, err := parseManifest([]byte(`{"name":"clock","version":"1","description":"A plugin.","permissions":[],"command":["x"],` + extra + `}`)); err == nil {
			t.Errorf("first format accepted %s", extra)
		}
	}
	// Categories are checked for form; ones not known here are kept but not
	// shown, so the market can add sections later.
	categorized, err := parseManifest([]byte(`{"name":"clock","version":"1","description":"Tells the time.","permissions":[],"categories":["example","later-section"],"command":["x"]}`))
	if err != nil || len(categorized.Categories) != 2 || !slices.Equal(ShownCategories(categorized.Categories), []string{"example"}) {
		t.Errorf("categories %q: %v", categorized.Categories, err)
	}
	for _, categories := range []string{`["a","b","c","d"]`, `["Desktop"]`, `["system","system"]`, `[""]`} {
		if _, err := parseManifest([]byte(`{"name":"clock","version":"1","description":"Tells the time.","permissions":[],"categories":` + categories + `,"command":["x"]}`)); err == nil {
			t.Errorf("categories %s accepted", categories)
		}
	}
	// A page alone, with nothing to call, is a plugin too.
	if _, err := parseManifest([]byte(`{"name":"notes","version":"1","description":"A plugin.","permissions":[],"engines":{"cage":">=0.1.0"},"kind":"host",
		"contributes":{"views":[{"id":"notes","title":"Notes","entry":"index.html"}]}}`)); err != nil {
		t.Errorf("static page: %v", err)
	}
	// A floating view declares presentation: "floating" with optional dimensions and needs >=0.2.1.
	floating, err := parseManifest([]byte(`{"name":"hud","version":"1","description":"A floating view.","permissions":[],"engines":{"cage":">=0.2.1"},"kind":"guest",
		"contributes":{"views":[{"id":"hud","title":"HUD","entry":"view/index.html","presentation":"floating","width":312,"height":260}]}}`))
	if err != nil || floating.Contributes.Views[0].Presentation != ViewPresentationFloating || floating.Contributes.Views[0].Width != 312 || floating.Contributes.Views[0].Height != 260 {
		t.Errorf("floating view: %+v %v", floating, err)
	}
	for name, raw := range map[string]string{
		"floating needs 0.2.1":      `{"name":"hud","version":"1","description":"A floating view.","permissions":[],"engines":{"cage":">=0.2.0"},"kind":"guest","contributes":{"views":[{"id":"hud","title":"HUD","entry":"view/index.html","presentation":"floating"}]}}`,
		"unknown presentation":      `{"name":"hud","version":"1","description":"A floating view.","permissions":[],"engines":{"cage":">=0.2.1"},"kind":"guest","contributes":{"views":[{"id":"hud","title":"HUD","entry":"view/index.html","presentation":"modal"}]}}`,
		"width on page view":        `{"name":"hud","version":"1","description":"A floating view.","permissions":[],"engines":{"cage":">=0.2.0"},"kind":"guest","contributes":{"views":[{"id":"hud","title":"HUD","entry":"view/index.html","width":320}]}}`,
		"width too small":           `{"name":"hud","version":"1","description":"A floating view.","permissions":[],"engines":{"cage":">=0.2.1"},"kind":"guest","contributes":{"views":[{"id":"hud","title":"HUD","entry":"view/index.html","presentation":"floating","width":120}]}}`,
		"height too large":          `{"name":"hud","version":"1","description":"A floating view.","permissions":[],"engines":{"cage":">=0.2.1"},"kind":"guest","contributes":{"views":[{"id":"hud","title":"HUD","entry":"view/index.html","presentation":"floating","height":900}]}}`,
	} {
		if _, err := parseManifest([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEngineRange(t *testing.T) {
	for _, c := range []struct {
		spec, version string
		want          bool
	}{
		{">=0.2.0", "0.2.0", true}, {">=0.2.0", "0.1.9", false}, {">=0.2.0", "3.0.0", true}, {">=0.2.0", "0.2.0-dev.abc", true},
		{"^1.2.0", "1.9.0", true}, {"^1.2.0", "2.0.0", false}, {"^1.2.0", "1.1.9", false},
		{"^0.2.3", "0.2.9", true}, {"^0.2.3", "0.3.0", false}, {"^0.0.3", "0.0.3", true}, {"^0.0.3", "0.0.4", false},
		{"1.0.0", "1.0.0", false},
	} {
		if got := engineSatisfied(c.spec, c.version); got != c.want {
			t.Errorf("%s with %s: %t", c.spec, c.version, got)
		}
	}
}

// Plugins this application cannot run are left unstarted without a failure.
// Link handlers are not in effect yet, so two plugins naming the same scheme
// do not clash.
func TestLoadPluginsLeavesOutWhatCannotRun(t *testing.T) {
	dir := t.TempDir()
	write := func(name, manifest string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, ManifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	v2 := func(name, kind, engine, scheme string) string {
		return `{"name":"` + name + `","version":"1","description":"A plugin.","permissions":[],"engines":{"cage":"` + engine + `"},"kind":"` + kind + `",
			"contributes":{"mcpServers":[{"id":"main","command":["/nonexistent/program"]}],"uriHandlers":[{"scheme":"` + scheme + `","server":"main","tool":"open"}]}}`
	}
	write("a-guest", v2("a-guest", "guest", ">=0.1.0", "alpha"))
	write("b-newer", v2("b-newer", "host", ">=9.0.0", "beta"))
	write("c-clash", v2("c-clash", "guest", ">=0.1.0", "alpha"))
	write("d-later", v2("d-later", "guest", ">=0.1.0", "beta"))
	plugins, failures := LoadPlugins(context.Background(), dir, LoadOptions{Version: "0.1.8"})
	if len(plugins) != 0 || len(failures) != 0 {
		t.Fatalf("plugins %d, failures %v", len(plugins), failures)
	}
}

// A plugin listed once for each target is offered as the one for this
// system; fields that are not well formed leave the entry out.
func TestMarketPicksTheEntryForThisSystem(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	entry := func(name, extra string) string {
		return `{"name":"` + name + `","version":"1","description":"","permissions":[],"archive":"PREFIX` + name + `.tar.gz","sha256":"` + digest + `","bytes":10` + extra + `}`
	}
	other := "win32-x64"
	if Target(KindHost) == other {
		other = "linux-arm64"
	}
	index := `{"version":1,"plugins":[` + strings.Join([]string{
		entry("tool", `,"target":"`+other+`"`),
		entry("tool", `,"target":"`+Target(KindHost)+`","displayName":"Tool","engines":{"cage":">=9.0.0"}`),
		entry("page", `,"kind":"guest","target":"`+Target(KindGuest)+`","engines":{"cage":">=0.1.0"}`),
		entry("bad-kind", `,"kind":"both"`),
		entry("bad-target", `,"target":"plan9-mips"`),
		entry("bad-engine", `,"engines":{"cage":"latest"}`),
	}, ",") + `]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(index, "PREFIX", "http://"+r.Host+"/dist/")))
	}))
	defer server.Close()
	market := Market{IndexURL: server.URL + "/index.json", ArchivePrefix: server.URL + "/dist/"}
	entries, err := market.Fetch(context.Background(), server.Client())
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries %+v: %v", entries, err)
	}
	tool, page := entries[0], entries[1]
	if tool.Target != Target(KindHost) || tool.DisplayName != "Tool" || tool.Kind != KindHost || !tool.Supported() || tool.EngineSatisfied("0.1.8") {
		t.Fatalf("tool %+v", tool)
	}
	if page.Kind != KindGuest || !page.Supported() || !page.EngineSatisfied("0.1.8") {
		t.Fatalf("page %+v", page)
	}
}

// A guest plugin runs through the command it is given a place for, and its
// tools are offered like any other's.
func TestLoadPluginsRunsGuestPluginsWhereTold(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	dir := t.TempDir()
	if err := InstallFromCatalog("clock", dir); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"clock","version":"1","description":"Tells the time.","permissions":[],"engines":{"cage":">=0.1.0"},"kind":"guest",
		"contributes":{"mcpServers":[{"id":"main","command":["python3","server.py"]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "clock", ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if plugins, failures := LoadPlugins(ctx, dir, LoadOptions{}); len(plugins) != 0 || len(failures) != 0 {
		t.Fatalf("ran a guest plugin with nowhere to run it: %d %v", len(plugins), failures)
	}
	var asked Manifest
	plugins, failures := LoadPlugins(ctx, dir, LoadOptions{Guest: func(_ context.Context, m Manifest, where string) (*exec.Cmd, error) {
		asked = m
		cmd := exec.Command(m.ServerCommand()[0], m.ServerCommand()[1:]...)
		cmd.Dir = where
		return cmd, nil
	}})
	if len(failures) != 0 || len(plugins) != 1 || asked.Name != "clock" {
		t.Fatalf("plugins %d, failures %v", len(plugins), failures)
	}
	defer plugins[0].Close()
	if result, err := plugins[0].call(ctx, "current_time", nil, false); err != nil || result.IsError {
		t.Fatalf("call: %v %+v", err, result)
	}
}

// With only guest plugins wanted, those on this computer are left unstarted.
func TestLoadPluginsCanLeaveHostPluginsOut(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	dir := t.TempDir()
	if err := InstallFromCatalog("clock", dir); err != nil {
		t.Fatal(err)
	}
	started := false
	plugins, failures := LoadPlugins(context.Background(), dir, LoadOptions{GuestOnly: true,
		Guest: func(context.Context, Manifest, string) (*exec.Cmd, error) { started = true; return nil, nil }})
	if len(plugins) != 0 || len(failures) != 0 || started {
		t.Fatalf("a plugin on this computer was started: %d %v", len(plugins), failures)
	}
}
