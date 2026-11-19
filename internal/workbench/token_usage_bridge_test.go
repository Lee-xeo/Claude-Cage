//go:build !windows

package workbench

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenUsageRealBridgeReadsOnlyGuestCounters(t *testing.T) {
	s := testServer(t)
	s.ready = true
	home := t.TempDir()
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TZ=UTC"})
	logs := filepath.Join(home, ".claude", "projects", "fixture")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"id":"msg_fixture","model":"claude-sonnet-4-6","content":"must-not-cross-bridge","usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":100,"cache_creation_input_tokens":2}}}` + "\n"
	path := filepath.Join(logs, "session.jsonl")
	if err := os.WriteFile(path, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	response := request(s, "POST", "/api/token-usage", s.origin, `{"action":"refresh"}`, true)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "must-not-cross") || strings.Contains(response.Body.String(), "msg_fixture") || strings.Contains(response.Body.String(), home) {
		t.Fatal("raw metadata crossed bridge", response.Body)
	}
	var view struct {
		Snapshot tokenUsageSnapshot `json:"snapshot"`
	}
	if json.Unmarshal(response.Body.Bytes(), &view) != nil || len(view.Snapshot.Days) != 1 || view.Snapshot.Days[0].Read != 100 {
		t.Fatal("real collector result lost", response.Body)
	}
	if data, _ := os.ReadFile(path); string(data) != line {
		t.Fatal("source log modified")
	}
}

func TestTokenUsageOfficialCLITranscript(t *testing.T) {
	binary := os.Getenv("CAGE_TEST_CLAUDE_CLI")
	if runtime.GOOS != "darwin" || !filepath.IsAbs(binary) {
		t.Skip("requires CAGE_TEST_CLAUDE_CLI and the macOS isolation sandbox")
	}
	binary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	home, project := t.TempDir(), t.TempDir()
	personal, _ := os.UserHomeDir()
	quote := func(value string) string { b, _ := json.Marshal(value); return string(b) }
	policy := "(version 1)(allow default)(deny mach-lookup)(deny file-read* (subpath " + quote(personal) + ") (subpath \"/Library/Keychains\"))(allow file-read* (literal " + quote(binary) + "))(deny network-outbound)(allow network-outbound (remote ip \"localhost:*\"))"
	var resumed atomic.Bool
	model := mobileTestModel(t, &resumed)
	defer model.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", policy, binary, "--bare", "--model", "claude-sonnet-4-6", "--tools", "", "--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "-p", "follow-up local usage fixture", "--output-format", "json")
	cmd.Dir = project
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=sk-ant-local-usage-fixture", "ANTHROPIC_BASE_URL=" + model.URL, "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1,localhost", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated official CLI: %v\n%s", err, output)
	}
	// Exercise the standard entrypoint field without launching a logged-in
	// Desktop app. All four runs use the same network/credential sandbox.
	for _, entrypoint := range []string{"claude-desktop", "local-agent", "claude-vscode"} {
		child := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
		child.Dir = cmd.Dir
		child.Env = append(append([]string{}, cmd.Env...), "CLAUDE_CODE_ENTRYPOINT="+entrypoint)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated entrypoint %s: %v\n%s", entrypoint, err, output)
		}
	}
	s := testServer(t)
	s.ready = true
	mobileTestSSH(t, s, home, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TZ=UTC"})
	response := request(s, "POST", "/api/token-usage", s.origin, `{"action":"refresh"}`, true)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	var view struct {
		Snapshot tokenUsageSnapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	var input, output, requests int64
	clients := map[string]int64{}
	for _, day := range view.Snapshot.Days {
		input += day.Input
		output += day.Output
		requests += day.Requests
		clients[day.Client] += day.Requests
	}
	if input != 32 || output != 20 || requests != 4 {
		t.Fatalf("official transcript counters: input=%d output=%d requests=%d invalid=%d", input, output, requests, view.Snapshot.Invalid)
	}
	for _, client := range []string{"desktop_code", "cowork", "ide", "automation"} {
		if clients[client] != 1 {
			t.Fatalf("official transcript classification %s: %+v", client, clients)
		}
	}
}
