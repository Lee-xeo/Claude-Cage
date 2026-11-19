package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The test binary doubles as a plugin program: with TEST_PLUGIN set it
// serves MCP on its standard input and output instead of running tests.
func TestMain(m *testing.M) {
	if kind := os.Getenv("TEST_PLUGIN"); kind != "" {
		servePlugin(kind)
		return
	}
	os.Exit(m.Run())
}

func servePlugin(kind string) {
	server := mcp.NewServer(&mcp.Implementation{Name: "p", Version: "1"}, nil)
	type input struct {
		Text string `json:"text"`
	}
	type output struct {
		Text string `json:"text"`
	}
	tool := &mcp.Tool{Name: "repeat_text", Description: "Repeat the given text."}
	switch kind {
	case "worded":
		tool.Description = "Repeat text from inside the VM."
	case "clash":
		tool.Name = "run_command"
	}
	mcp.AddTool(server, tool, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
		if kind == "dies" && in.Text == "die" {
			os.Exit(3)
		}
		return nil, output{Text: in.Text + in.Text}, nil
	})
	if kind == "audiences" {
		// One tool for the page alone, one for the model alone.
		for name, audience := range map[string]string{"page_action": "app", "model_action": "model"} {
			only := &mcp.Tool{Name: name, Description: "Do one thing.", Meta: mcp.Meta{"ui": map[string]any{"visibility": []any{audience}}}}
			mcp.AddTool(server, only, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
				return nil, output{Text: name}, nil
			})
		}
	}
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
}

// A tool says who it is for. One for the plugin's page is not offered to
// the model, and takes no tool name from other plugins; one for the model
// is not the page's to call.
func TestToolsAreOfferedOnlyToWhomTheyAreFor(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "mixed", "audiences")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plugins, failures := LoadPlugins(ctx, root, LoadOptions{Taken: []string{"page_action"}})
	if len(failures) != 0 || len(plugins) != 1 {
		t.Fatalf("plugins %d, failures %v", len(plugins), failures)
	}
	plugin := plugins[0]
	defer plugin.Close()
	if names := plugin.Tools(); !slices.Equal(slices.Sorted(slices.Values(names)), []string{"model_action", "repeat_text"}) {
		t.Fatalf("offered to the model: %v", names)
	}
	var page []string
	for _, tool := range plugin.PageTools() {
		page = append(page, tool.Name)
	}
	if !slices.Equal(slices.Sorted(slices.Values(page)), []string{"page_action", "repeat_text"}) {
		t.Fatalf("offered to the page: %v", page)
	}
	for _, info := range plugin.ToolList() {
		if info.PageOnly != (info.Name == "page_action") {
			t.Fatalf("%s page only: %t", info.Name, info.PageOnly)
		}
	}
	if result, err := plugin.CallFromView(ctx, "page_action", json.RawMessage(`{"text":"x"}`), time.Minute); err != nil || result.IsError {
		t.Fatalf("the page's own tool: %v %+v", err, result)
	}
	var refused *RefusedError
	if _, err := plugin.CallFromView(ctx, "model_action", json.RawMessage(`{"text":"x"}`), time.Minute); !errors.As(err, &refused) {
		t.Fatalf("the page called a tool that is the model's alone: %v", err)
	}
	// What the model is given, as a client of the server sees it.
	token, _ := NewToken()
	bridge, err := Start(token, plugin)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	session, err := bridge.connect(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "page_action" {
			t.Fatal("the page's tool was offered to the model")
		}
	}
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "page_action", Arguments: map[string]any{"text": "x"}}); err == nil && !result.IsError {
		t.Fatal("the model called the page's tool")
	}
}

// writePlugin makes a plugin directory whose program is this test binary.
func writePlugin(t *testing.T, root, name, kind string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the plugin program is a shell script")
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nTEST_PLUGIN=" + kind + " exec " + os.Args[0] + "\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(Manifest{Name: name, Version: "1.0.0", Description: "d", Permissions: []string{}, Command: []string{"run.sh"}})
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPluginToolIsOfferedAndCalledAfterApproval(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "repeater", "plain")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var asked []ToolRequest
	refuse := false
	plugins, failures := LoadPlugins(ctx, root, LoadOptions{Taken: []string{"run_command"}, Approve: func(_ context.Context, request ToolRequest) error {
		asked = append(asked, request)
		if refuse {
			return errors.New("the user refused this call")
		}
		return nil
	}})
	if len(failures) != 0 || len(plugins) != 1 {
		t.Fatalf("plugins %d, failures %v", len(plugins), failures)
	}
	defer plugins[0].Close()
	token, _ := NewToken()
	bridge, err := Start(token, Command{Dirs: func() []string { return []string{root} }}, plugins[0])
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	session, err := bridge.connect(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "repeat_text,run_command" && strings.Join(names, ",") != "run_command,repeat_text" {
		t.Fatalf("tools %v", names)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "repeat_text", Arguments: map[string]string{"text": "ab"}})
	if err != nil || result.IsError {
		t.Fatalf("call: %v %+v", err, result)
	}
	if text, _ := json.Marshal(result.StructuredContent); !strings.Contains(string(text), "abab") {
		t.Fatalf("result %s", text)
	}
	if len(asked) != 1 || asked[0].Plugin != "repeater" || asked[0].Tool != "repeat_text" || !strings.Contains(asked[0].Arguments, `"ab"`) {
		t.Fatalf("asked %+v", asked)
	}
	refuse = true
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "repeat_text", Arguments: map[string]string{"text": "cd"}})
	if err != nil || !result.IsError {
		t.Fatalf("a refused call returned %v %+v", err, result)
	}
	if text, _ := json.Marshal(result.Content); !strings.Contains(string(text), "the user refused this call") || strings.Contains(string(text), "cdcd") {
		t.Fatalf("refusal %s", text)
	}
}

func TestPluginsThatBreakTheRulesAreLeftOut(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "good", "plain")
	writePlugin(t, root, "worded", "worded")
	writePlugin(t, root, "clash", "clash")
	misnamed := writePlugin(t, root, "misnamed", "plain")
	if err := os.Rename(misnamed, filepath.Join(root, "other-name")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "not-a-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plugins, failures := LoadPlugins(ctx, root, LoadOptions{Taken: []string{"run_command"}})
	for _, plugin := range plugins {
		defer plugin.Close()
	}
	if len(plugins) != 1 || plugins[0].Manifest.Name != "good" {
		t.Fatalf("loaded %d plugins", len(plugins))
	}
	text := ""
	for _, failure := range failures {
		text += failure.Error() + "\n"
	}
	for _, want := range []string{`mentions "vm"`, "run_command is already in use", "must be named after the plugin"} {
		if !strings.Contains(text, want) {
			t.Errorf("no failure says %q:\n%s", want, text)
		}
	}
	if len(failures) != 3 {
		t.Errorf("%d failures:\n%s", len(failures), text)
	}
}

func TestPluginProgramIsRunAgainAfterItExits(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "fragile", "dies")
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plugin := &External{Manifest: manifest, Dir: dir}
	if err := plugin.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer plugin.Close()
	_, _ = plugin.call(ctx, "repeat_text", json.RawMessage(`{"text":"die"}`), false)
	result, err := plugin.call(ctx, "repeat_text", json.RawMessage(`{"text":"ok"}`), false)
	if err != nil || result.IsError {
		t.Fatalf("after the program exited: %v %+v", err, result)
	}
	if text, _ := json.Marshal(result.StructuredContent); !strings.Contains(string(text), "okok") {
		t.Fatalf("result %s", text)
	}
}

func TestManifestMustBeComplete(t *testing.T) {
	for name, content := range map[string]string{
		"bad name":           `{"name":"Bad Name","version":"1","description":"A plugin.","permissions":[],"command":["x"]}`,
		"no version":         `{"name":"a","permissions":[],"command":["x"]}`,
		"no command":         `{"name":"a","version":"1","description":"A plugin.","permissions":[]}`,
		"no permissions":     `{"name":"a","version":"1","command":["x"]}`,
		"unknown permission": `{"name":"a","version":"1","description":"A plugin.","permissions":["everything"],"command":["x"]}`,
		"unknown platform":   `{"name":"a","version":"1","description":"A plugin.","permissions":[],"platforms":["beos"],"command":["x"]}`,
		"unknown field":      `{"name":"a","version":"1","description":"A plugin.","permissions":[],"command":["x"],"autostart":true}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(dir); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"name":"screen-reader","version":"0.1.0","description":"d","platforms":["darwin"],"permissions":["screen"],"command":["./bin/reader"]}`), 0o644)
	manifest, err := LoadManifest(dir)
	if err != nil || manifest.Supported() != (runtime.GOOS == "darwin") {
		t.Fatalf("manifest %+v: %v", manifest, err)
	}
}

// The example in the plugin guide must load as written.
func TestCatalogPluginInstallsAndLoads(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	catalog := Catalog()
	if len(catalog) == 0 || catalog[0].Name != "clock" {
		t.Fatalf("catalog %+v", catalog)
	}
	if err := InstallFromCatalog("clock", dir); err != nil {
		t.Fatal(err)
	}
	if err := InstallFromCatalog("clock", dir); err == nil {
		t.Fatal("replaced an installed plugin")
	}
	if err := InstallFromCatalog("missing", dir); err == nil {
		t.Fatal("installed an unknown plugin")
	}
	if plugins, failures := LoadPlugins(ctx, dir, LoadOptions{Off: []string{"clock"}}); len(plugins) != 0 || len(failures) != 0 {
		t.Fatalf("started a plugin that is turned off: %d %v", len(plugins), failures)
	}
	plugins, failures := LoadPlugins(ctx, dir, LoadOptions{Taken: []string{"run_command"}})
	if len(failures) != 0 || len(plugins) != 1 {
		t.Fatalf("plugins %d, failures %v", len(plugins), failures)
	}
	defer plugins[0].Close()
	result, err := plugins[0].call(ctx, "current_time", nil, false)
	if err != nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("call: %v %+v", err, result)
	}
}
