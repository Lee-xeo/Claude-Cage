//go:build !windows

package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/google/uuid"
)

func TestClaudeSwitchUnopenedSessionWhenSourceLoggedOut(t *testing.T) {
	s, _, project := metadataFixture(t)
	s.ready = true
	s.terminals.sessions = nil
	s.projectShares = map[string]*projectShare{project: {done: make(chan struct{})}}
	home, err := filepath.EvalSymlinks(settledTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Fake claude CLI: answers `claude agents --json` from $HOME/agents.json.
	const cli = "#!/bin/sh\nif [ \"$#\" -eq 2 ] && [ \"$1\" = agents ] && [ \"$2\" = --json ]; then\n  cat \"$HOME/agents.json\"\n  exit 0\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(cli), 0700); err != nil {
		t.Fatal(err)
	}
	// Minimal python stub for /usr/local/bin/cage-claude-account so cli_channel.py launch() works cleanly.
	const accountStub = "#!/usr/bin/env python3\nimport json, os, pathlib, sys\ndef profile(name): return pathlib.Path.home() / '.local/share/cage/claude-accounts' / name\ndef has_status_line(base): return True\nif __name__ == '__main__':\n    pathlib.Path(os.environ['HOME'], 'launched.json').write_text(json.dumps(sys.argv[1:]))\n"
	stubPath := filepath.Join(bin, "cage-claude-account")
	if err := os.WriteFile(stubPath, []byte(accountStub), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("export PATH="+shellescape.Quote(bin)+":/usr/bin:/bin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	guestEnv := []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	mobileTestSSHRewrite(t, s, home, guestEnv, func(cmd string) string {
		return strings.ReplaceAll(cmd, "/usr/local/bin/cage-claude-account", stubPath)
	})
	// The fake guest is this host: the helper runs on its python3 through a
	// login shell. A real guest answers in a second or two, but a loaded CI
	// host can take longer than the product's per-attempt deadline, so the
	// test gives each attempt more time. The interpreter is started once
	// first, outside any deadline; each step is logged for failure diagnosis.
	previousTimeout := cliChannelTimeout
	cliChannelTimeout = 2 * time.Minute
	t.Cleanup(func() { cliChannelTimeout = previousTimeout })
	for _, step := range [][]string{
		{"/usr/bin/python3", "-I", "-c", ""},
		{"/usr/bin/python3", "-I", "-c", ""},
		{"/bin/bash", "-lc", "exec python3 -I -c ''"},
	} {
		started := time.Now()
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir, cmd.Env = home, guestEnv
		output, err := cmd.CombinedOutput()
		t.Logf("guest interpreter %q: %v err=%v output=%q", step, time.Since(started).Round(time.Millisecond), err, output)
	}

	a, b := strings.Repeat("a", 24), strings.Repeat("b", 24)
	list := claudeAccountList{Accounts: []claudeAccount{
		{ID: a, Label: "Account A", Email: "a@example.test", Enabled: true, Status: "signed_out", HandoffGroup: "team"},
		{ID: b, Label: "Account B", Email: "b@example.test", Enabled: true, Status: "ready", HandoffGroup: "team"},
	}}
	if err := s.saveClaudeAccounts(list); err != nil {
		t.Fatal(err)
	}
	var probedSource atomic.Int32
	s.claudeAccounts.probe = func(_ context.Context, guestID string) (claudeGuestSnapshot, error) {
		if guestID == a {
			probedSource.Add(1)
			return claudeGuestSnapshot{}, errors.New("source account signed out")
		}
		if guestID == b {
			return claudeGuestSnapshot{LoggedIn: true, Email: "b@example.test"}, nil
		}
		return claudeGuestSnapshot{}, errors.New("unexpected guest id")
	}

	// Place an unopened session and file-history checkpoint under Account A.
	sourceCLIID := uuid.NewString()
	sourceConfig := filepath.Join(home, ".local/share/cage/claude-accounts", a, "config")
	historyPath := filepath.Join(sourceConfig, "projects", "fixture", sourceCLIID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(historyPath), 0700); err != nil {
		t.Fatal(err)
	}
	row, _ := json.Marshal(map[string]any{"cwd": guestPath(project), "type": "user", "message": map[string]string{"content": "unopened work from A"}})
	originalHistory := append(row, '\n')
	if err := os.WriteFile(historyPath, originalHistory, 0600); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(sourceConfig, "file-history", sourceCLIID, "snap@v1")
	if err := os.MkdirAll(filepath.Dir(checkpoint), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, []byte("before-edit"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(home, "agents.json")
	if err := writePrivate(registry, []any{}); err != nil {
		t.Fatal(err)
	}

	listStarted := time.Now()
	listResp := request(s, "GET", "/api/cli/sessions", s.origin, "", true)
	t.Logf("first session list (helper install and retry): %v", time.Since(listStarted).Round(time.Millisecond))
	t.Cleanup(func() {
		if t.Failed() {
			logCLIChannelDiagnostics(t, s.profile)
		}
	})
	if listResp.Code != 200 {
		t.Fatalf("list sessions failed: %d %s", listResp.Code, listResp.Body.String())
	}
	var discoveredID string
	for id := range s.mobileCLI.catalog {
		record, err := s.mobileCLI.record(s.profile, id)
		if err == nil && record.CLIID == sourceCLIID {
			discoveredID = id
			if record.Launcher != "" || record.Connection != "" || record.Account.ID != a || record.AccountLabel != "Account A" {
				t.Fatalf("unexpected discovered record: launcher=%q conn=%q account=%+v label=%q", record.Launcher, record.Connection, record.Account, record.AccountLabel)
			}
		}
	}
	if discoveredID == "" {
		t.Fatalf("did not discover unopened session from Account A (response: %s)", listResp.Body.String())
	}

	// Inspect must report supported=true, unopened=true, and candidate B even though A is signed_out.
	inspectResp := request(s, "POST", "/api/cli/sessions/"+discoveredID+"/account", s.origin, `{"action":"inspect"}`, true)
	if inspectResp.Code != 200 {
		t.Fatalf("inspect failed: %d %s", inspectResp.Code, inspectResp.Body.String())
	}
	var inspected struct {
		Supported  bool                 `json:"supported"`
		Unopened   bool                 `json:"unopened"`
		SourceID   string               `json:"source_id"`
		Candidates []claudeSwitchOption `json:"candidates"`
	}
	if err := json.Unmarshal(inspectResp.Body.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if !inspected.Supported || !inspected.Unopened || inspected.SourceID != sourceCLIID || len(inspected.Candidates) != 1 || inspected.Candidates[0].ID != b {
		t.Fatalf("unexpected inspect payload: %+v", inspected)
	}

	// Active process holding Account A's session must be refused without touching ownership.
	if err := writePrivate(registry, []map[string]any{{"sessionId": sourceCLIID, "pid": os.Getpid()}}); err != nil {
		t.Fatal(err)
	}
	blockedReqID := uuid.NewString()
	blockedBody, _ := json.Marshal(claudeSwitchInput{Action: "switch", Target: b, RequestID: blockedReqID, SourceID: sourceCLIID, Confirm: true})
	blockedResp := request(s, "POST", "/api/cli/sessions/"+discoveredID+"/account", s.origin, string(blockedBody), true)
	if blockedResp.Code != 409 || !strings.Contains(blockedResp.Body.String(), "正在运行") {
		t.Fatalf("expected active-session rejection, got %d %s", blockedResp.Code, blockedResp.Body.String())
	}
	// Cancel the blocked preparing attempt cleanly.
	cancelBody, _ := json.Marshal(claudeSwitchInput{Action: "cancel", RequestID: blockedReqID})
	cancelResp := request(s, "POST", "/api/cli/sessions/"+discoveredID+"/account", s.origin, string(cancelBody), true)
	if cancelResp.Code != 200 {
		t.Fatalf("cancel failed: %d %s", cancelResp.Code, cancelResp.Body.String())
	}

	// Once no process owns the session, continuing directly in Account B succeeds without probing A.
	if err := writePrivate(registry, []any{}); err != nil {
		t.Fatal(err)
	}
	reqID := uuid.NewString()
	switchBody, _ := json.Marshal(claudeSwitchInput{Action: "switch", Target: b, RequestID: reqID, SourceID: sourceCLIID, Confirm: true})
	switchResp := request(s, "POST", "/api/cli/sessions/"+discoveredID+"/account", s.origin, string(switchBody), true)
	if switchResp.Code != 200 {
		t.Fatalf("switch failed: %d %s", switchResp.Code, switchResp.Body.String())
	}
	if probedSource.Load() != 0 {
		t.Fatalf("source account should not be probed when unopened, got %d probes", probedSource.Load())
	}
	// Original history in Account A must remain preserved untouched.
	afterHistory, err := os.ReadFile(historyPath)
	if err != nil || string(afterHistory) != string(originalHistory) {
		t.Fatalf("source history modified or missing: %v %q", err, afterHistory)
	}
	// Frozen switch directory must contain history + file-history + empty-connection receipt.
	switchDir := filepath.Join(home, ".local/share/cage/cli-account-switches", reqID)
	frozenCheckpoint, err := os.ReadFile(filepath.Join(switchDir, "file-history", sourceCLIID, "snap@v1"))
	if err != nil || string(frozenCheckpoint) != "before-edit" {
		t.Fatalf("frozen file-history missing: %v %q", err, frozenCheckpoint)
	}
	var receipt struct {
		Binding struct {
			CLIID      string `json:"cli_id"`
			Connection string `json:"connection"`
		} `json:"binding"`
	}
	rawReceipt, err := os.ReadFile(filepath.Join(switchDir, "receipt.json"))
	if err != nil || json.Unmarshal(rawReceipt, &receipt) != nil || receipt.Binding.CLIID != sourceCLIID || receipt.Binding.Connection != "" {
		t.Fatalf("unexpected switch receipt: %v %s", err, rawReceipt)
	}
	updated, err := s.mobileCLI.record(s.profile, discoveredID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Account.ID != b || updated.Launcher != "claude" || len(updated.Executions) != 1 || updated.Executions[0].CLIID != sourceCLIID {
		t.Fatalf("unexpected updated record: %+v", updated)
	}
}

// logCLIChannelDiagnostics reports the channel's own records (operation,
// failure, elapsed_ms, shared) for a failed run.
func logCLIChannelDiagnostics(t *testing.T, profile string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(profile, "logs", "*"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "cli-channel") {
				t.Logf("diagnostics: %s", line)
			}
		}
	}
}

func TestMatchDiscoveredAccountSupportsDefaultAndManaged(t *testing.T) {
	defID, managedID := strings.Repeat("1", 24), strings.Repeat("2", 24)
	list := claudeAccountList{Accounts: []claudeAccount{
		{ID: defID, Label: "Default", Source: "default", Email: "def@example.test"},
		{ID: managedID, Label: "Managed", Source: "managed", Email: "m@example.test"},
	}}
	sel, label := matchDiscoveredAccount(list, "/home/cage/.claude")
	if sel.ID != defID || sel.GuestID != "default" || label != "Default" || sel.Subject != claudeIdentityHash("def@example.test", "") {
		t.Fatalf("default account did not match: %+v %q", sel, label)
	}
	sel, label = matchDiscoveredAccount(list, "/home/cage/.local/share/cage/claude-accounts/"+managedID+"/config")
	if sel.ID != managedID || sel.GuestID != managedID || label != "Managed" || sel.Subject != claudeIdentityHash("m@example.test", "") {
		t.Fatalf("managed account did not match: %+v %q", sel, label)
	}
}
