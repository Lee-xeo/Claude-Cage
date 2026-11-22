package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	case "prompts":
		tool.Name = "echo_note"
	case "prompt_override":
		tool.Name = "override_tool"
	case "prompt_worded":
		tool.Name = "worded_prompt_tool"
	case "prompt_result_worded":
		tool.Name = "leaky_prompt_tool"
	case "prompt_clash":
		tool.Name = "clash_prompt_tool"
	case "page_only_all":
		tool.Name = "ui_only_tool"
		tool.Meta = mcp.Meta{"ui": map[string]any{"visibility": []any{"app"}}}
	}
	mcp.AddTool(server, tool, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
		if kind == "dies" && in.Text == "die" {
			os.Exit(3)
		}
		return nil, output{Text: in.Text + in.Text}, nil
	})
	switch kind {
	case "prompts":
		server.AddPrompt(&mcp.Prompt{
			Name:        "summarize_note",
			Description: "Summarize a note cleanly.",
			Arguments:   []*mcp.PromptArgument{{Name: "topic", Description: "Topic to focus on."}},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			topic := ""
			if req != nil && req.Params != nil {
				topic = req.Params.Arguments["topic"]
			}
			return &mcp.GetPromptResult{
				Description: "Summarize a note cleanly.",
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: "Summarize topic: " + topic},
				}},
			}, nil
		})
	case "prompt_override":
		server.AddPrompt(&mcp.Prompt{
			Name:        "custom-prompt",
			Description: "Plugin-defined slash prompt.",
		}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: "Plugin-defined slash prompt.",
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: "Custom override message"},
				}},
			}, nil
		})
	case "prompt_worded":
		server.AddPrompt(&mcp.Prompt{
			Name:        "bad_prompt",
			Description: "Inspect files inside the VM.",
		}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	case "prompt_result_worded":
		server.AddPrompt(&mcp.Prompt{
			Name:        "leaky_prompt",
			Description: "Inspect files.",
		}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: "Run inside the guest container."},
				}},
			}, nil
		})
	case "prompt_clash":
		server.AddPrompt(&mcp.Prompt{
			Name:        "commands",
			Description: "Clashes with built-in prompt.",
		}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{}, nil
		})
	}
	if kind == "project" {
		where := &mcp.Tool{Name: "where_am_i", Description: "Report the folder and process."}
		mcp.AddTool(server, where, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
			if in.Text == "slow" {
				time.Sleep(700 * time.Millisecond)
			}
			return nil, output{Text: fmt.Sprintf("%s|%d", strings.Join(os.Args[1:], " "), os.Getpid())}, nil
		})
	}
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

func TestExpandCommandProjectDir(t *testing.T) {
	if !CommandUsesProjectDir([]string{"python3", "launcher.py", "--allow-dir", ProjectDirPlaceholder}) {
		t.Fatal("expected CommandUsesProjectDir to detect placeholder")
	}
	if CommandUsesProjectDir([]string{ProjectDirPlaceholder, "arg"}) {
		t.Fatal("executable name must not count as project dir placeholder")
	}
	cmd, err := ExpandCommand([]string{"python3", "launcher.py", "--allow-dir", ProjectDirPlaceholder, "--root=" + ProjectDirPlaceholder}, "/work/app")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"python3", "launcher.py", "--allow-dir", "/work/app", "--root=/work/app"}
	if !slices.Equal(cmd, want) {
		t.Fatalf("expanded %v, want %v", cmd, want)
	}
	// Never replace command[0].
	cmd0, err := ExpandCommand([]string{ProjectDirPlaceholder, "arg"}, "/work/app")
	if err != nil || cmd0[0] != ProjectDirPlaceholder {
		t.Fatalf("executable was altered: %v %v", cmd0, err)
	}
	// Missing project dir fails when placeholder is used, succeeds otherwise.
	if _, err := ExpandCommand([]string{"python3", "launcher.py", ProjectDirPlaceholder}, ""); !errors.Is(err, ErrNoProjectDir) {
		t.Fatalf("expected ErrNoProjectDir, got %v", err)
	}
	plain, err := ExpandCommand([]string{"python3", "server.py"}, "")
	if err != nil || !slices.Equal(plain, []string{"python3", "server.py"}) {
		t.Fatalf("plain command failed: %v %v", plain, err)
	}
}

func TestMissingProgramErrorAndMissingProjectDir(t *testing.T) {
	dir := t.TempDir()
	missingBin := "cage-nonexistent-runtime-xyz"
	pluginDir := filepath.Join(dir, "missing-rt")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ManifestFile), []byte(
		`{"name":"missing-rt","version":"0.1.0","description":"Needs missing runtime","permissions":[],"command":["`+missingBin+`","run"]}`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	plugins, failures := LoadPlugins(context.Background(), dir, LoadOptions{})
	if len(plugins) != 0 || len(failures) != 1 {
		t.Fatalf("plugins %d failures %v", len(plugins), failures)
	}
	var missing *MissingProgramError
	if !errors.As(failures[0], &missing) || missing.Plugin != "missing-rt" || missing.Program != missingBin {
		t.Fatalf("expected MissingProgramError, got %T: %v", failures[0], failures[0])
	}
	for program, want := range map[string]string{
		"uv":      "https://docs.astral.sh/uv/",
		"uvx":     "https://docs.astral.sh/uv/",
		"npx":     "https://nodejs.org/",
		"node":    "https://nodejs.org/",
		"python3": "https://www.python.org/",
	} {
		msg := (&MissingProgramError{Plugin: "p", Program: program}).Error()
		if !strings.Contains(msg, want) {
			t.Errorf("%s hint %q missing %q", program, msg, want)
		}
	}

	// Plugin requiring ${PROJECT_DIR} fails cleanly when no project is open.
	projDir := filepath.Join(dir, "needs-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, ManifestFile), []byte(
		`{"name":"needs-project","version":"0.1.0","description":"Scoped to project","permissions":["files"],"command":["python3","server.py","--allow-dir","${PROJECT_DIR}"]}`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	_, failures = LoadPlugins(context.Background(), dir, LoadOptions{Off: []string{"missing-rt"}})
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %v", failures)
	}
	var noProj *MissingProjectDirError
	if !errors.As(failures[0], &noProj) || noProj.Plugin != "needs-project" {
		t.Fatalf("expected MissingProjectDirError, got %T: %v", failures[0], failures[0])
	}
}

// writeProjectPlugin makes a plugin whose program reports the folder it was
// given for ${PROJECT_DIR}, and its process.
func writeProjectPlugin(t *testing.T, root, name string, args ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the plugin program is a shell script")
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nTEST_PLUGIN=project exec " + os.Args[0] + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(Manifest{Name: name, Version: "1.0.0", Description: "d", Permissions: []string{}, Command: append([]string{"run.sh"}, args...)})
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func whereAmI(t *testing.T, ctx context.Context, plugin *External, text string) (string, error) {
	t.Helper()
	result, err := plugin.call(ctx, "where_am_i", json.RawMessage(`{"text":"`+text+`"}`), false)
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", fmt.Errorf("tool error: %+v", result.Content)
	}
	var out struct{ Text string }
	data, _ := json.Marshal(result.StructuredContent)
	_ = json.Unmarshal(data, &out)
	return out.Text, nil
}

func TestRestartRunsThePluginForTheNewProject(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	writeProjectPlugin(t, root, "scoped", "--root", "${PROJECT_DIR}")
	writeProjectPlugin(t, root, "plain", "--fixed")

	// No project open: the scoped plugin is reported and kept, not running.
	plugins, failures := LoadPlugins(ctx, root, LoadOptions{})
	defer func() {
		for _, plugin := range plugins {
			plugin.Close()
		}
	}()
	if len(plugins) != 2 || len(failures) != 1 {
		t.Fatalf("plugins %d failures %v", len(plugins), failures)
	}
	if _, ok := errors.AsType[*MissingProjectDirError](failures[0]); !ok {
		t.Fatalf("failure %v", failures[0])
	}
	byName := map[string]*External{}
	for _, plugin := range plugins {
		byName[plugin.Manifest.Name] = plugin
	}
	scoped, plain := byName["scoped"], byName["plain"]
	if scoped.Running() || len(scoped.Tools()) != 0 || !scoped.UsesProjectDir() || !plain.Running() || plain.UsesProjectDir() {
		t.Fatalf("scoped running=%v tools=%v plain running=%v", scoped.Running(), scoped.Tools(), plain.Running())
	}

	// A project opens: the waiting plugin starts with it.
	if err := scoped.Restart(ctx, func() string { return "/p/one" }); err != nil {
		t.Fatal(err)
	}
	first, err := whereAmI(t, ctx, scoped, "")
	if err != nil || !strings.HasPrefix(first, "--root /p/one|") {
		t.Fatalf("first project: %q %v", first, err)
	}
	plainBefore, _ := whereAmI(t, ctx, plain, "")

	// Switching projects restarts it with the new folder while a call to
	// another plugin carries on.
	slow := make(chan error, 1)
	go func() {
		_, err := whereAmI(t, ctx, plain, "slow")
		slow <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if err := scoped.Restart(ctx, func() string { return "/p/two" }); err != nil {
		t.Fatal(err)
	}
	second, err := whereAmI(t, ctx, scoped, "")
	if err != nil || !strings.HasPrefix(second, "--root /p/two|") || second == first {
		t.Fatalf("second project: %q %v", second, err)
	}
	if err := <-slow; err != nil {
		t.Fatalf("other plugin's call was interrupted: %v", err)
	}
	if plainAfter, _ := whereAmI(t, ctx, plain, ""); plainAfter != plainBefore {
		t.Fatalf("plugin without the placeholder restarted: %q -> %q", plainBefore, plainAfter)
	}

	// No shared project: it stops and offers nothing rather than running
	// somewhere else.
	err = scoped.Restart(ctx, func() string { return "" })
	if _, ok := errors.AsType[*MissingProjectDirError](err); !ok {
		t.Fatalf("restart without a project: %v", err)
	}
	if scoped.Running() || len(scoped.Tools()) != 0 {
		t.Fatalf("still running=%v tools=%v", scoped.Running(), scoped.Tools())
	}
}

func TestPluginPromptsAreForwardedAndFallbackGenerated(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "plain-helper", "plain")
	writePlugin(t, root, "notes-helper", "prompts")
	writePlugin(t, root, "custom-prompt", "prompt_override")
	writePlugin(t, root, "page-only", "page_only_all")
	writePlugin(t, root, "bad-prompt", "prompt_worded")
	writePlugin(t, root, "leaky-prompt", "prompt_result_worded")
	writePlugin(t, root, "clash-prompt", "prompt_clash")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plugins, failures := LoadPlugins(ctx, root, LoadOptions{
		Taken:        []string{"run_command"},
		TakenPrompts: []string{"commands"},
	})
	for _, plugin := range plugins {
		defer plugin.Close()
	}

	failureText := ""
	for _, err := range failures {
		failureText += err.Error() + "\n"
	}
	for _, want := range []string{`prompt bad_prompt mentions "vm"`, "prompt name commands is already in use"} {
		if !strings.Contains(failureText, want) {
			t.Errorf("expected failure containing %q, got:\n%s", want, failureText)
		}
	}
	if len(failures) != 2 {
		t.Fatalf("expected 2 failures, got %d:\n%s", len(failures), failureText)
	}

	bridgePlugins := []Plugin{Command{Dirs: func() []string { return []string{root} }}}
	for _, p := range plugins {
		bridgePlugins = append(bridgePlugins, p)
	}
	token, _ := NewToken()
	bridge, err := Start(token, bridgePlugins...)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	session, err := bridge.connect(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	promptNames := map[string]*mcp.Prompt{}
	for _, p := range listed.Prompts {
		promptNames[p.Name] = p
		if word, found := CheckPromptWording(p); found {
			t.Errorf("prompt %s mentions reserved word %q", p.Name, word)
		}
	}
	for _, want := range []string{"commands", "plain-helper", "notes-helper", "summarize_note", "custom-prompt", "leaky-prompt", "leaky_prompt"} {
		if promptNames[want] == nil {
			t.Errorf("missing expected prompt %q in %v", want, promptNames)
		}
	}
	if promptNames["page-only"] != nil {
		t.Error("page-only plugin must not generate a model-facing prompt")
	}

	// Automatic plugin prompt guides the model to the plugin's tool with the request.
	autoRes, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "plain-helper",
		Arguments: map[string]string{"request": "echo hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := autoRes.Messages[0].Content.(*mcp.TextContent); !ok || !strings.Contains(text.Text, "repeat_text") || !strings.Contains(text.Text, "echo hello") {
		t.Fatalf("unexpected auto prompt body: %#v", autoRes.Messages[0].Content)
	}

	// Native plugin prompt forwards arguments to the child server.
	nativeRes, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "summarize_note",
		Arguments: map[string]string{"topic": "architecture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := nativeRes.Messages[0].Content.(*mcp.TextContent); !ok || text.Text != "Summarize topic: architecture" {
		t.Fatalf("unexpected native prompt body: %#v", nativeRes.Messages[0].Content)
	}

	// Plugin-supplied prompt with the plugin's own name overrides the automatic fallback.
	overrideRes, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "custom-prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := overrideRes.Messages[0].Content.(*mcp.TextContent); !ok || text.Text != "Custom override message" {
		t.Fatalf("unexpected override prompt body: %#v", overrideRes.Messages[0].Content)
	}

	// Runtime prompt output containing a reserved word is rejected.
	if _, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "leaky_prompt"}); err == nil {
		t.Fatal("expected leaky_prompt returning reserved wording to be refused")
	}
}
