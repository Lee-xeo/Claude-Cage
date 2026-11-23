package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

// The test binary doubles as a plugin program: with TEST_PLUGIN_SERVE set it
// serves one MCP tool reporting its arguments and process, after an
// optional start-up delay.
func TestMain(m *testing.M) {
	if os.Getenv("TEST_PLUGIN_SERVE") != "" {
		if delay, err := time.ParseDuration(os.Getenv("TEST_PLUGIN_DELAY")); err == nil {
			time.Sleep(delay)
		}
		server := mcp.NewServer(&mcp.Implementation{Name: "p", Version: "1"}, nil)
		type input struct {
			Slow bool `json:"slow"`
		}
		type output struct {
			Text string `json:"text"`
		}
		tool := &mcp.Tool{Name: os.Getenv("TEST_PLUGIN_SERVE") + "_where", Description: "Report the folder and process."}
		mcp.AddTool(server, tool, func(_ context.Context, _ *mcp.CallToolRequest, in input) (*mcp.CallToolResult, output, error) {
			if in.Slow {
				time.Sleep(800 * time.Millisecond)
			}
			return nil, output{Text: fmt.Sprintf("%s|%d", strings.Join(os.Args[1:], " "), os.Getpid())}, nil
		})
		_ = server.Run(context.Background(), &mcp.StdioTransport{})
		return
	}
	// No Mihomo is installed beside the test binary; proxy tests replace the
	// core itself and only assert the missing-core path explicitly.
	proxyCorePath = func() (string, error) { return "mihomo", nil }
	os.Exit(m.Run())
}

func writeTestPlugin(t *testing.T, root, name, delay string, args ...string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nTEST_PLUGIN_SERVE=%s TEST_PLUGIN_DELAY=%s exec %s \"$@\"\n", name, delay, os.Args[0])
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(hostbridge.Manifest{Name: name, Version: "1.0.0", Description: "d", Permissions: []string{}, Command: append([]string{"run.sh"}, args...)})
	if err := os.WriteFile(filepath.Join(dir, hostbridge.ManifestFile), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
}

func callWhere(ctx context.Context, plugin *hostbridge.External, slow bool) (string, error) {
	result, err := plugin.CallFromView(ctx, plugin.Manifest.Name+"_where", json.RawMessage(fmt.Sprintf(`{"slow":%t}`, slow)), 30*time.Second)
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", fmt.Errorf("tool error %+v", result.Content)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var out struct{ Text string }
	_ = json.Unmarshal(data, &out)
	return out.Text, nil
}

// Switching projects restarts only plugins taking ${PROJECT_DIR}, in the
// background: the request returns at once, other plugins and their calls in
// progress carry on, and an unshared workspace never lends the placeholder
// another project's folder.
func TestProjectSwitchRestartsOnlyProjectPluginsInTheBackground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the plugin program is a shell script")
	}
	root := t.TempDir()
	writeTestPlugin(t, root, "scoped", "400ms", "--root", "${PROJECT_DIR}")
	writeTestPlugin(t, root, "plain", "0s", "--fixed")
	s := &Server{
		profile:       t.TempDir(),
		preferences:   Preferences{HostTools: true, Workspace: "/p/a"},
		projectShares: map[string]*projectShare{"/p/a": {}, "/p/b": {}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plugins, failures := hostbridge.LoadPlugins(ctx, root, hostbridge.LoadOptions{ProjectDir: s.hostActiveProjectDir()})
	if len(plugins) != 2 || len(failures) != 0 {
		t.Fatalf("plugins %d failures %v", len(plugins), failures)
	}
	tools := &hostTools{ctx: ctx, cancel: cancel, plugins: plugins, serving: true, pluginProblems: map[string]string{}}
	s.hostTools = tools
	defer func() {
		for _, plugin := range plugins {
			plugin.Close()
		}
	}()
	named := map[string]*hostbridge.External{}
	for _, plugin := range plugins {
		named[plugin.Manifest.Name] = plugin
	}
	scoped, plain := named["scoped"], named["plain"]
	before, err := callWhere(ctx, scoped, false)
	if err != nil || !strings.HasPrefix(before, "--root /p/a|") {
		t.Fatalf("first project: %q %v", before, err)
	}
	plainBefore, _ := callWhere(ctx, plain, false)

	slow := make(chan error, 1)
	go func() { _, err := callWhere(ctx, plain, true); slow <- err }()
	time.Sleep(100 * time.Millisecond)
	s.mu.Lock()
	s.preferences.Workspace = "/p/b"
	s.mu.Unlock()
	started := time.Now()
	done := s.restartProjectPlugins()
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("restartProjectPlugins blocked for %v", elapsed)
	}
	select {
	case <-done:
		t.Fatal("restart finished before the plugin could have started: it did not run in the background")
	default:
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("restart did not finish")
	}
	after, err := callWhere(ctx, scoped, false)
	if err != nil || !strings.HasPrefix(after, "--root /p/b|") {
		t.Fatalf("after switching: %q %v", after, err)
	}
	if err := <-slow; err != nil {
		t.Fatalf("a call to another plugin was interrupted: %v", err)
	}
	if plainAfter, _ := callWhere(ctx, plain, false); plainAfter != plainBefore {
		t.Fatalf("plugin without the placeholder restarted: %q -> %q", plainBefore, plainAfter)
	}

	// The current workspace is not shared from here: the plugin stops with
	// a reason instead of running for /p/a or /p/b.
	s.mu.Lock()
	s.preferences.Workspace = "/env/default"
	s.mu.Unlock()
	<-s.restartProjectPlugins()
	if scoped.Running() || len(scoped.Tools()) != 0 {
		t.Fatalf("scoped still running for an unshared workspace: %v", scoped.Tools())
	}
	s.mu.Lock()
	problem := tools.pluginProblems["scoped"]
	s.mu.Unlock()
	if problem != hostPluginNeedsProject {
		t.Fatalf("problem %q", problem)
	}
	view := s.hostToolsView()
	for _, plugin := range view.Plugins {
		if plugin.Name == "scoped" && (plugin.Loaded || plugin.Problem != hostPluginNeedsProject) {
			t.Fatalf("view %+v", plugin)
		}
	}
	if len(view.Problems) != 1 || view.Problems[0] != "scoped："+hostPluginNeedsProject {
		t.Fatalf("problems %q", view.Problems)
	}

	// Opening a shared project again brings it back.
	s.mu.Lock()
	s.preferences.Workspace = "/p/a"
	s.mu.Unlock()
	<-s.restartProjectPlugins()
	if got, err := callWhere(ctx, scoped, false); err != nil || !strings.HasPrefix(got, "--root /p/a|") {
		t.Fatalf("back on a project: %q %v", got, err)
	}
	s.mu.Lock()
	_, stale := tools.pluginProblems["scoped"]
	s.mu.Unlock()
	if stale || len(s.hostToolsView().Problems) != 0 {
		t.Fatal("the problem was not cleared")
	}
}
