package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBridgeEphemeralProxyLifecycleAndCollisionGuard(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	baseCmd := Command{Dirs: func() []string { return []string{dir} }, Jobs: NewJobs()}
	defer baseCmd.Jobs.Close()

	bridge, err := Start(token, baseCmd)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	taken := bridge.BaseToolNames()
	if !taken["run_command"] || !taken["command_job"] {
		t.Fatalf("expected base tools in taken map, got %v", taken)
	}
	takenPrompts := bridge.BasePromptNames()
	if !takenPrompts["commands"] {
		t.Fatalf("expected base commands prompt in takenPrompts, got %v", takenPrompts)
	}

	var calledWith string
	proxy, err := NewProxyPlugin([]*mcp.Tool{
		{Name: "run_command", Description: "Conflicting command tool"},
		{Name: "cage_secret", Description: "Mentions reserved word"},
		{Name: "page_only", Description: "Only for page", Meta: mcp.Meta{"ui": map[string]any{"visibility": []any{"app"}}}},
		{Name: "local_render", Description: "Render the active scene"},
	}, taken, func(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
		calledWith = name + ":" + string(args)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "rendered-ok"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy.SetPluginPrompts([]ProxyPromptGroup{
		{Name: "commands", DisplayName: "Shadow attempt", Tools: []string{"local_render"}},
		{Name: "blender", DisplayName: "Blender", Tools: []string{"local_render"}},
	}, takenPrompts)
	if got := proxy.Tools(); len(got) != 1 || got[0] != "local_render" {
		t.Fatalf("unexpected accepted proxy tools: %v", got)
	}
	if got := proxy.Prompts(); len(got) != 1 || got[0] != "blender" {
		t.Fatalf("unexpected accepted proxy prompts: %v", got)
	}
	if len(proxy.Skipped()) != 3 {
		t.Fatalf("expected 3 skipped tools, got %v", proxy.Skipped())
	}

	bridge.SetEphemeral(proxy)

	ctx := context.Background()
	session, err := bridge.connect(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	if !names["run_command"] || !names["local_render"] || names["cage_secret"] || names["page_only"] {
		t.Fatalf("unexpected listed tools: %v", names)
	}

	listedPrompts, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	promptNames := map[string]bool{}
	for _, prompt := range listedPrompts.Prompts {
		promptNames[prompt.Name] = true
	}
	if !promptNames["commands"] || !promptNames["blender"] {
		t.Fatalf("unexpected listed prompts: %v", promptNames)
	}
	blenderPrompt, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "blender",
		Arguments: map[string]string{"request": "render frame 7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := blenderPrompt.Messages[0].Content.(*mcp.TextContent); !ok || !strings.Contains(text.Text, "local_render") || !strings.Contains(text.Text, "render frame 7") {
		t.Fatalf("unexpected proxied prompt body: %#v", blenderPrompt.Messages[0].Content)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "local_render", Arguments: map[string]any{"frame": 7}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) == 0 || calledWith != `local_render:{"frame":7}` {
		t.Fatalf("unexpected proxy call result: res=%+v called=%q", res, calledWith)
	}

	// Clearing ephemeral plugins withdraws proxied tools and prompts immediately.
	bridge.SetEphemeral()
	listedAfter, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listedAfter.Tools {
		if tool.Name == "local_render" {
			t.Fatal("proxied tool remained visible after SetEphemeral()")
		}
	}
	promptsAfter, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range promptsAfter.Prompts {
		if prompt.Name == "blender" {
			t.Fatal("proxied prompt remained visible after SetEphemeral()")
		}
	}
}

func TestProxyPluginReturnsToolErrorOnCallFailure(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := Start(token)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	proxy, err := NewProxyPlugin([]*mcp.Tool{{Name: "fail_tool", Description: "Fails intentionally"}}, nil,
		func(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
			return nil, errors.New("the user did not approve this request, so nothing was run")
		})
	if err != nil {
		t.Fatal(err)
	}
	bridge.SetEphemeral(proxy)

	ctx := context.Background()
	session, err := bridge.connect(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "fail_tool"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true when proxy handler returns an error")
	}
}

func TestArchiveAndInstallPluginRoundTripAndSecurity(t *testing.T) {
	srcRoot := t.TempDir()
	pluginDir := filepath.Join(srcRoot, "demo-tool")
	if err := os.MkdirAll(filepath.Join(pluginDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"name":"demo-tool","version":"1.2.0","description":"Demo tool","engines":{"cage":">=0.1.0"},"kind":"guest","permissions":[],"contributes":{"mcpServers":[{"id":"main","command":["python3","server.py"]}]}}`
	if err := os.WriteFile(filepath.Join(pluginDir, ManifestFile), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "server.py"), []byte("print('ok')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "sub", "data.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive1, digest1, err := ArchivePlugin(pluginDir, "demo-tool")
	if err != nil {
		t.Fatal(err)
	}
	archive2, digest2, err := ArchivePlugin(pluginDir, "demo-tool")
	if err != nil {
		t.Fatal(err)
	}
	if digest1 == "" || digest1 != digest2 || len(archive1) != len(archive2) {
		t.Fatalf("expected deterministic archive digest: %q vs %q", digest1, digest2)
	}

	destRoot := t.TempDir()
	installed, err := InstallArchive(archive1, "demo-tool", destRoot, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "demo-tool" || installed.Version != "1.2.0" {
		t.Fatalf("unexpected installed manifest: %+v", installed)
	}
	// Never overwrites existing folder directly.
	if _, err := InstallArchive(archive1, "demo-tool", destRoot, "0.2.0"); err == nil {
		t.Fatal("expected error when target plugin folder already exists")
	}
	// Digest matches after round-trip unpack.
	installedDigest, err := PluginDigest(filepath.Join(destRoot, "demo-tool"), "demo-tool")
	if err != nil {
		t.Fatal(err)
	}
	if installedDigest != digest1 {
		t.Fatalf("round-trip digest mismatch: %q vs %q", installedDigest, digest1)
	}

	// Reject symlinks when packing.
	if runtime.GOOS != "windows" {
		if err := os.Symlink("/etc/hosts", filepath.Join(pluginDir, "link")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ArchivePlugin(pluginDir, "demo-tool"); err == nil {
			t.Fatal("expected symlink to be rejected during ArchivePlugin")
		}
	}
}

func TestVersionCompareAndPlatformHelpers(t *testing.T) {
	if cmp, ok := CompareVersions("1.2.0", "1.1.9"); !ok || cmp <= 0 {
		t.Fatalf("expected 1.2.0 > 1.1.9, got %d %v", cmp, ok)
	}
	if cmp, ok := CompareVersions("0.2.0-rc.1", "0.2.0"); !ok || cmp != 0 {
		t.Fatalf("expected prerelease core equal, got %d %v", cmp, ok)
	}
	if _, ok := CompareVersions("invalid", "1.0.0"); ok {
		t.Fatal("expected invalid version to fail")
	}
	if !SupportsPlatform(Manifest{Kind: KindGuest, Platforms: []string{"darwin"}}, "linux") {
		t.Fatal("guest plugins must always support linux")
	}
	if SupportsPlatform(Manifest{Kind: KindHost, Platforms: []string{"darwin"}}, "linux") {
		t.Fatal("darwin-only host plugin must not support linux")
	}
	if !RequiresGUI(Manifest{Kind: KindHost, Permissions: []string{"screen"}}) {
		t.Fatal("host plugin with screen permission requires GUI")
	}
	if RequiresGUI(Manifest{Kind: KindGuest, Permissions: []string{"screen"}}) {
		t.Fatal("guest plugin should not be flagged as host GUI")
	}
}
