package hostbridge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDefaultPluginsBundledAndDigestMatchesDeployed(t *testing.T) {
	defaults := DefaultPlugins()
	if len(defaults) < 2 {
		t.Fatalf("expected at least two bundled default plugins, got %d", len(defaults))
	}
	studio, ok := DefaultPluginByName("cage-plugin")
	if !ok {
		t.Fatal("expected cage-plugin in bundled defaults")
	}
	if studio.Manifest.Kind != KindGuest {
		t.Fatalf("kind = %q, want %q", studio.Manifest.Kind, KindGuest)
	}
	if len(studio.Manifest.Permissions) != 0 {
		t.Fatalf("permissions = %v, want empty", studio.Manifest.Permissions)
	}
	if len(studio.I18n) != 15 {
		t.Fatalf("i18n languages = %d, want 15", len(studio.I18n))
	}

	dir := t.TempDir()
	digest, err := InstallDefaultPlugin("cage-plugin", dir)
	if err != nil {
		t.Fatalf("InstallDefaultPlugin: %v", err)
	}
	if digest != studio.Digest {
		t.Fatalf("digest mismatch: installed %s vs embedded %s", digest, studio.Digest)
	}
	deployedDigest, err := PluginDigest(filepath.Join(dir, "cage-plugin"), "cage-plugin")
	if err != nil {
		t.Fatalf("PluginDigest: %v", err)
	}
	if deployedDigest != studio.Digest {
		t.Fatalf("deployed PluginDigest %s != embedded %s", deployedDigest, studio.Digest)
	}
	// Installing again must not overwrite an existing folder.
	if _, err := InstallDefaultPlugin("cage-plugin", dir); err == nil {
		t.Fatal("expected error when default plugin already exists")
	}

	ctrl, ok := DefaultPluginByName("use-cage")
	if !ok {
		t.Fatal("expected use-cage in bundled defaults")
	}
	if ctrl.Manifest.Kind != KindHost || len(ctrl.Manifest.Permissions) != 0 {
		t.Fatalf("unexpected use-cage manifest: %+v", ctrl.Manifest)
	}
	if len(ctrl.I18n) != 15 {
		t.Fatalf("use-cage i18n languages = %d, want 15", len(ctrl.I18n))
	}
	ctrlDigest, err := InstallDefaultPlugin("use-cage", dir)
	if err != nil {
		t.Fatalf("InstallDefaultPlugin(use-cage): %v", err)
	}
	deployedCtrlDigest, err := PluginDigest(filepath.Join(dir, "use-cage"), "use-cage")
	if err != nil || deployedCtrlDigest != ctrlDigest || ctrlDigest != ctrl.Digest {
		t.Fatalf("use-cage digest mismatch: %s vs %s vs %s (%v)", deployedCtrlDigest, ctrlDigest, ctrl.Digest, err)
	}
}

func TestUseCageToolWordingIsNeutral(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	installRoot := t.TempDir()
	if _, err := InstallDefaultPlugin("use-cage", installRoot); err != nil {
		t.Fatal(err)
	}
	ctrlDir := filepath.Join(installRoot, "use-cage")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, python, filepath.Join(ctrlDir, "server.py"))
	cmd.Dir = ctrlDir
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect use-cage: %v", err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listed.Tools) != 5 {
		t.Fatalf("got %d tools, want 5", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if word, found := CheckWording(tool); found {
			t.Fatalf("use-cage tool %s violates CheckWording with %q", tool.Name, word)
		}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "workspace_list_capabilities", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("workspace_list_capabilities failed: %v %+v", err, res)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if word, found := CheckWording(&mcp.Tool{Name: "cap", Description: tc.Text}); found {
				t.Fatalf("workspace_list_capabilities leaks reserved word %q", word)
			}
		}
	}
}

func TestCagePluginSearchAndCreateEndToEndAndNeutrality(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	installRoot := t.TempDir()
	if _, err := InstallDefaultPlugin("cage-plugin", installRoot); err != nil {
		t.Fatal(err)
	}
	studioDir := filepath.Join(installRoot, "cage-plugin")
	draftsDir := t.TempDir()
	submissionsDir := t.TempDir()
	catalogPath := filepath.Join(t.TempDir(), "market-catalog.json")
	sampleCatalog := `{
  "updated_at": "2026-11-22T17:20:00Z",
  "plugins": [
    {
      "name": "blender",
      "display_name": "Blender",
      "version": "1.0.0",
      "description": "Connect to a running Blender instance for 3D scene modeling.",
      "categories": ["desktop", "productivity"],
      "permissions": ["network"],
      "target": "local",
      "installed": false,
      "supported": true,
      "has_mcp": true
    },
    {
      "name": "excel-workbooks",
      "display_name": "Excel Workbooks",
      "version": "1.2.0",
      "description": "Read and edit Excel spreadsheet workbooks.",
      "categories": ["files"],
      "permissions": [],
      "target": "workspace",
      "installed": true,
      "installed_version": "1.2.0",
      "supported": true,
      "has_mcp": true
    }
  ]
}`
	if err := os.WriteFile(catalogPath, []byte(sampleCatalog), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, python, filepath.Join(studioDir, "server.py"))
	cmd.Dir = studioDir
	cmd.Env = append(os.Environ(),
		"PLUGIN_DRAFTS_DIR="+draftsDir,
		"PLUGIN_SUBMISSIONS_DIR="+submissionsDir,
		"PLUGIN_MARKET_CATALOG="+catalogPath,
	)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect cage-plugin: %v", err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listed.Tools) != 6 {
		t.Fatalf("got %d tools, want 6", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		if word, found := CheckWording(tool); found {
			t.Fatalf("tool %s violates CheckWording with reserved word %q", tool.Name, word)
		}
	}

	callText := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool %s: %v", name, err)
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return sb.String(), res.IsError
	}

	// 1) Search finds a ready-made market plugin first.
	marketOut, isErr := callText("search_plugins", map[string]any{"query": "3D blender"})
	if isErr || !strings.Contains(marketOut, `"blender"`) || !strings.Contains(marketOut, "Plugins Market") {
		t.Fatalf("unexpected blender search result: err=%v out=%s", isErr, marketOut)
	}
	// 2) Search recognizes an already installed plugin.
	instOut, isErr := callText("search_plugins", map[string]any{"query": "spreadsheet"})
	if isErr || !strings.Contains(instOut, `"excel-workbooks"`) || !strings.Contains(instOut, "already installed") {
		t.Fatalf("unexpected installed search result: err=%v out=%s", isErr, instOut)
	}
	// 3) Search with no match guides creating a custom plugin.
	missOut, isErr := callText("search_plugins", map[string]any{"query": "quantum-banana"})
	if isErr || !strings.Contains(missOut, "plugin_scaffold") {
		t.Fatalf("expected creation guidance on miss: err=%v out=%s", isErr, missOut)
	}

	// Verify the returned guide never exposes reserved environment/product wording.
	guide, isErr := callText("plugin_get_guide", map[string]any{})
	if isErr || guide == "" {
		t.Fatalf("plugin_get_guide failed: err=%v text=%q", isErr, guide)
	}
	if word, found := CheckWording(&mcp.Tool{Name: "guide", Description: guide}); found {
		t.Fatalf("plugin_get_guide output leaks reserved word %q", word)
	}

	// Scaffold a Python MCP + view plugin.
	scaffoldOut, isErr := callText("plugin_scaffold", map[string]any{
		"name":         "demo-widget",
		"template":     "mcp-with-view-python",
		"display_name": "Demo Widget",
		"description":  "Shows a simple timestamp status panel.",
	})
	if isErr {
		t.Fatalf("plugin_scaffold error: %s", scaffoldOut)
	}

	// Validate the freshly scaffolded plugin (includes live MCP smoke test).
	valOut, isErr := callText("plugin_validate", map[string]any{"name": "demo-widget"})
	if isErr {
		t.Fatalf("plugin_validate failed: %s", valOut)
	}
	var report struct {
		OK     bool     `json:"ok"`
		Errors []string `json:"errors"`
		Tools  []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(valOut), &report); err != nil {
		t.Fatalf("decode validation report: %v\n%s", err, valOut)
	}
	if !report.OK || len(report.Tools) != 1 || report.Tools[0].Name != "demo_widget_run" {
		t.Fatalf("unexpected validation report: %+v", report)
	}

	// Inject an inline script to confirm CSP validation catches it.
	htmlPath := filepath.Join(draftsDir, "demo-widget", "view", "index.html")
	origHTML, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(htmlPath, append(origHTML, []byte("<script>alert(1)</script>")...), 0o644); err != nil {
		t.Fatal(err)
	}
	badOut, _ := callText("plugin_validate", map[string]any{"name": "demo-widget"})
	if !strings.Contains(badOut, "inline <script>") {
		t.Fatalf("expected CSP inline script error, got: %s", badOut)
	}
	// Restore clean HTML and submit.
	if err := os.WriteFile(htmlPath, origHTML, 0o644); err != nil {
		t.Fatal(err)
	}
	subOut, isErr := callText("plugin_submit_draft", map[string]any{
		"name":    "demo-widget",
		"summary": "Initial demo widget with sidebar view",
	})
	if isErr {
		t.Fatalf("plugin_submit_draft failed: %s", subOut)
	}

	// Host-side ValidateDraftDir should translate extension.json into valid plugin.json.
	submittedFiles := filepath.Join(submissionsDir, "demo-widget", "files")
	manifest, manifestBytes, err := ValidateDraftDir(submittedFiles, "demo-widget", "", "")
	if err != nil {
		t.Fatalf("ValidateDraftDir: %v", err)
	}
	if manifest.Kind != KindGuest || manifest.Engines == nil || manifest.Engines.Cage != DefaultDraftEngineRange {
		t.Fatalf("unexpected translated manifest: %+v", manifest)
	}
	if !strings.Contains(string(manifestBytes), `"kind": "guest"`) {
		t.Fatalf("manifest bytes missing guest kind: %s", manifestBytes)
	}

	// Host override to kind=host works too.
	hostManifest, _, err := ValidateDraftDir(submittedFiles, "demo-widget", KindHost, "")
	if err != nil {
		t.Fatalf("ValidateDraftDir(host): %v", err)
	}
	if hostManifest.Kind != KindHost {
		t.Fatalf("kind = %q, want host", hostManifest.Kind)
	}

	// Symlink inside draft directory must be rejected on host side.
	if err := os.Symlink("/etc/hosts", filepath.Join(submittedFiles, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateDraftDir(submittedFiles, "demo-widget", "", ""); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestParseDraftManifestRejectsUnknownFields(t *testing.T) {
	bad := []byte(`{
		"name": "bad-ext",
		"version": "0.1.0",
		"description": "Test",
		"target": "workspace",
		"permissions": [],
		"engines": {"cage": ">=0.2.0"},
		"contributes": {"mcpServers": [{"id": "s", "command": ["python3", "server.py"]}]}
	}`)
	if _, err := ParseDraftManifest(bad); err == nil {
		t.Fatal("expected unknown field 'engines' to be rejected in neutral extension.json")
	}
}

func TestTranslateDraftManifestEngineFollowsViewPresentation(t *testing.T) {
	parse := func(view string) DraftManifest {
		draft, err := ParseDraftManifest([]byte(`{"name":"hud","version":"1","description":"A view.","target":"workspace","contributes":{"views":[` + view + `]}}`))
		if err != nil {
			t.Fatal(err)
		}
		return draft
	}
	page, _, err := TranslateDraftManifest(parse(`{"id":"hud","title":"HUD","entry":"view/index.html"}`), "", "")
	if err != nil || page.Engines.Cage != DefaultDraftEngineRange {
		t.Fatalf("page draft: %+v %v", page.Engines, err)
	}
	// 0.2.0 does not read view presentation, so a floating draft must not
	// claim to run there.
	floating, _, err := TranslateDraftManifest(parse(`{"id":"hud","title":"HUD","entry":"view/index.html","presentation":"floating","width":312}`), "", "")
	if err != nil || floating.Engines.Cage != ">=0.2.1" {
		t.Fatalf("floating draft: %+v %v", floating.Engines, err)
	}
	if _, _, err := TranslateDraftManifest(parse(`{"id":"hud","title":"HUD","entry":"view/index.html","presentation":"floating"}`), "", ">=0.2.0"); err == nil {
		t.Fatal("an explicit older engine range accepted a floating view")
	}
}
