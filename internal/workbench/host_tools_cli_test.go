//go:build !windows

package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

// TestClaudeRealCLIMCPPromptsAndRelay exercises the real official Claude Code
// binary inside a credential-isolated, offline macOS sandbox against a live
// hostbridge.Bridge over both:
//  1. Direct HTTP MCP (as configured for Claude Code in .claude.json), and
//  2. The Python stdio relay (as configured for Claude Desktop & numbered
//     Desktop profiles in claude_desktop_config.json).
func TestClaudeRealCLIMCPPromptsAndRelay(t *testing.T) {
	binary := os.Getenv("CAGE_TEST_CLAUDE_CLI")
	if !filepath.IsAbs(binary) || runtime.GOOS != "darwin" {
		t.Skip("requires an absolute CAGE_TEST_CLAUDE_CLI and macOS sandbox-exec")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}

	home, project, pluginRoot := t.TempDir(), t.TempDir(), t.TempDir()
	// Install the real clock plugin into an isolated plugin directory.
	clockSrc, err := os.ReadFile(filepath.Join("..", "hostbridge", "catalog", "clock", "server.py"))
	if err != nil {
		t.Fatal(err)
	}
	clockDir := filepath.Join(pluginRoot, "clock")
	if err := os.MkdirAll(clockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clockDir, "server.py"), clockSrc, 0o644); err != nil {
		t.Fatal(err)
	}
	clockManifest := `{"name":"clock","displayName":"Clock","version":"0.1.0","description":"Tells the current local time.","permissions":[],"command":["python3","server.py"]}`
	if err := os.WriteFile(filepath.Join(clockDir, hostbridge.ManifestFile), []byte(clockManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	loaded, failures := hostbridge.LoadPlugins(ctx, pluginRoot, hostbridge.LoadOptions{
		Taken:        []string{"run_command", "command_job"},
		TakenPrompts: []string{hostCommandsPlugin},
	})
	if len(failures) != 0 || len(loaded) != 1 {
		t.Fatalf("LoadPlugins: loaded=%d failures=%v", len(loaded), failures)
	}
	defer loaded[0].Close()

	jobs := hostbridge.NewJobs()
	defer jobs.Close()
	token, err := hostbridge.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := hostbridge.Start(token,
		hostbridge.Command{Dirs: func() []string { return []string{project} }, Jobs: jobs},
		loaded[0],
	)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	// Prepare both default Desktop and a numbered Desktop profile in the isolated home.
	numberedID := "0123456789abcdef01234567"
	defaultDesktopDir := filepath.Join(home, ".config", "Claude")
	numberedDesktopDir := filepath.Join(home, ".local", "share", "desktop-profiles", numberedID, "data")
	if err := os.MkdirAll(defaultDesktopDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(numberedDesktopDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Run the real host_tools_config.py script pointing at our live bridge address.
	entryJSON, _ := json.Marshal(map[string]any{
		"type":    "http",
		"url":     "http://" + bridge.Addr() + "/",
		"headers": map[string]string{"Authorization": "Bearer " + token},
	})
	cfgCmd := exec.CommandContext(ctx, python, "-", "computer", "add")
	cfgCmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	cfgCmd.Stdin = strings.NewReader(hostToolsConfigInput(entryJSON))
	if out, err := cfgCmd.CombinedOutput(); err != nil {
		t.Fatalf("host_tools_config.py failed: %v\n%s", err, out)
	}

	httpCfgPath := filepath.Join(home, ".claude.json")
	desktopCfgPath := filepath.Join(numberedDesktopDir, "claude_desktop_config.json")
	if _, err := os.Stat(desktopCfgPath); err != nil {
		t.Fatalf("numbered desktop config was not written: %v", err)
	}

	// Synthetic Anthropic /v1/messages endpoint that records everything Claude Code sends
	// and calls the MCP tool on the first turn so we verify the full prompt -> tool loop.
	var mu sync.Mutex
	var capturedBodies []string
	var nextToolName string
	var nextToolInput string

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages/count_tokens" {
			_, _ = io.WriteString(w, `{"input_tokens":7}`)
			return
		}
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var input struct {
			Messages json.RawMessage `json:"messages"`
			Model    string          `json:"model"`
		}
		if json.Unmarshal(raw, &input) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		mu.Lock()
		capturedBodies = append(capturedBodies, string(raw))
		toolToCall := nextToolName
		toolInput := nextToolInput
		mu.Unlock()

		hasResult := strings.Contains(string(input.Messages), `"tool_result"`)
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(kind string, fields map[string]any) {
			fields["type"] = kind
			b, _ := json.Marshal(fields)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b)
			w.(http.Flusher).Flush()
		}
		event("message_start", map[string]any{"message": map[string]any{
			"id": "msg_" + uuid.NewString(), "type": "message", "role": "assistant",
			"model": input.Model, "content": []string{}, "stop_reason": nil,
			"usage": map[string]int{"input_tokens": 8, "output_tokens": 1},
		}})
		stop := "end_turn"
		if !hasResult && toolToCall != "" {
			stop = "tool_use"
			event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
				"type": "tool_use", "id": "toolu_" + uuid.NewString(), "name": toolToCall, "input": map[string]any{},
			}})
			event("content_block_delta", map[string]any{"index": 0, "delta": map[string]string{
				"type": "input_json_delta", "partial_json": toolInput,
			}})
		} else {
			event("content_block_start", map[string]any{"index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
			event("content_block_delta", map[string]any{"index": 0, "delta": map[string]string{"type": "text_delta", "text": "done"}})
		}
		event("content_block_stop", map[string]any{"index": 0})
		event("message_delta", map[string]any{"delta": map[string]string{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 5}})
		event("message_stop", map[string]any{})
	}))
	defer fixture.Close()

	proxy, ca := mobileOfficialTestProxy(t, fixture)

	configDir := filepath.Join(home, "cli-profile")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	saveJSON := func(path string, v any) {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saveJSON(filepath.Join(configDir, ".claude.json"), map[string]any{
		"hasCompletedOnboarding": true,
		"oauthAccount": map[string]string{
			"accountUuid":      uuid.NewString(),
			"emailAddress":     "isolated-mcp@example.invalid",
			"organizationUuid": uuid.NewString(),
		},
	})
	saveJSON(filepath.Join(configDir, ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "sk-ant-oat01-synthetic-mcp",
			"refreshToken":     "synthetic",
			"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
			"scopes":           []string{"user:inference", "user:profile"},
			"subscriptionType": "pro",
		},
	})

	realHome, _ := os.UserHomeDir()
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	policy := "(version 1)(allow default)(deny mach-lookup)(deny file-read* (subpath " + quote(realHome) + ") (subpath \"/Library/Keychains\"))(allow file-read* (literal " + quote(binary) + "))(deny network-outbound)(allow network-outbound (remote ip \"localhost:*\"))"

	runCLI := func(mcpConfigPath, prompt string) (string, []string) {
		t.Helper()
		mu.Lock()
		capturedBodies = nil
		mu.Unlock()
		args := []string{
			"-p", policy, binary,
			"--model", "sonnet",
			"-p",
			"--output-format", "stream-json",
			"--verbose",
			"--setting-sources", "",
			"--strict-mcp-config",
			"--mcp-config", mcpConfigPath,
			"--dangerously-skip-permissions",
			"--max-turns", "3",
			prompt,
		}
		cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", args...)
		cmd.Dir = project
		cmd.Env = []string{
			"HOME=" + home,
			"PATH=/usr/bin:/bin",
			"CLAUDE_CONFIG_DIR=" + configDir,
			"HTTP_PROXY=" + proxy,
			"HTTPS_PROXY=" + proxy,
			"NO_PROXY=localhost,127.0.0.1",
			"NODE_EXTRA_CA_CERTS=" + ca,
			"SSL_CERT_FILE=" + ca,
			"DISABLE_AUTOUPDATER=1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("claude CLI failed for %q: %v\n%s", prompt, err, out)
		}
		mu.Lock()
		bodies := append([]string(nil), capturedBodies...)
		mu.Unlock()
		return string(out), bodies
	}

	parseInit := func(streamOut string) map[string]any {
		t.Helper()
		for _, line := range strings.Split(streamOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var ev map[string]any
			if json.Unmarshal([]byte(line), &ev) == nil && ev["type"] == "system" && ev["subtype"] == "init" {
				return ev
			}
		}
		t.Fatalf("no system/init event in output:\n%s", streamOut)
		return nil
	}

	// 1. Direct HTTP MCP (Claude Code path) + explicit slash command for clock plugin.
	mu.Lock()
	nextToolName = "mcp__computer__current_time"
	nextToolInput = `{}`
	mu.Unlock()
	httpOut, httpBodies := runCLI(httpCfgPath, "/mcp__computer__clock what time is it")
	httpInit := parseInit(httpOut)
	t.Logf("HTTP init mcp_servers=%v slash_commands=%v", httpInit["mcp_servers"], httpInit["slash_commands"])
	initJSON, _ := json.Marshal(httpInit)
	for _, want := range []string{`"name":"computer"`, `"status":"connected"`, `"mcp__computer__clock"`, `"mcp__computer__commands"`, `"mcp__computer__current_time"`, `"mcp__computer__run_command"`} {
		if !strings.Contains(string(initJSON), want) {
			t.Fatalf("HTTP init missing %q:\n%s", want, initJSON)
		}
	}
	if len(httpBodies) < 2 {
		t.Fatalf("expected prompt turn + tool_result turn over HTTP, got %d requests", len(httpBodies))
	}
	if !strings.Contains(httpBodies[0], "Use the available tools (current_time)") || !strings.Contains(httpBodies[0], "what time is it") {
		t.Fatalf("HTTP slash prompt did not expand in /v1/messages:\n%s", httpBodies[0])
	}
	// Verify real clock/server.py returned an ISO timestamp back to the model.
	if !strings.Contains(httpBodies[1], "tool_result") || !strings.Contains(httpBodies[1], "T") {
		t.Fatalf("HTTP clock tool_result missing in second turn:\n%s", httpBodies[1])
	}

	// 2. Numbered Desktop config (stdio relay.py path) + explicit slash command for commands plugin.
	mu.Lock()
	nextToolName = "mcp__computer__run_command"
	nextToolInput = `{"command":"echo desktop-relay-ok"}`
	mu.Unlock()
	relayOut, relayBodies := runCLI(desktopCfgPath, "/mcp__computer__commands run echo")
	relayInit := parseInit(relayOut)
	t.Logf("Desktop Relay init mcp_servers=%v slash_commands=%v", relayInit["mcp_servers"], relayInit["slash_commands"])
	relayInitJSON, _ := json.Marshal(relayInit)
	for _, want := range []string{`"name":"computer"`, `"status":"connected"`, `"mcp__computer__clock"`, `"mcp__computer__commands"`, `"mcp__computer__run_command"`} {
		if !strings.Contains(string(relayInitJSON), want) {
			t.Fatalf("Relay init missing %q:\n%s", want, relayInitJSON)
		}
	}
	if len(relayBodies) < 2 {
		t.Fatalf("expected prompt turn + tool_result turn over Desktop relay, got %d requests", len(relayBodies))
	}
	if !strings.Contains(relayBodies[0], "Use the available tools (command_job, run_command)") || !strings.Contains(relayBodies[0], "run echo") {
		t.Fatalf("Relay slash prompt did not expand in /v1/messages:\n%s", relayBodies[0])
	}
	if !strings.Contains(relayBodies[1], "desktop-relay-ok") {
		t.Fatalf("Relay run_command output missing in second turn:\n%s", relayBodies[1])
	}

	// Verify no reserved product/environment wording leaked into the computer MCP tool schemas or expanded prompts.
	for idx, body := range []string{httpBodies[0], relayBodies[0]} {
		var req struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("parse captured request %d: %v", idx, err)
		}
		for _, tool := range req.Tools {
			if strings.Contains(string(tool), "mcp__computer__") {
				if word, found := hostbridge.CheckTextWording(string(tool)); found {
					t.Fatalf("computer tool schema leaked reserved word %q: %s", word, tool)
				}
			}
		}
		for _, msg := range req.Messages {
			if strings.Contains(string(msg), "Use the available tools (") {
				if word, found := hostbridge.CheckTextWording(string(msg)); found {
					t.Fatalf("expanded slash prompt leaked reserved word %q: %s", word, msg)
				}
			}
		}
	}
}
