//go:build !windows

package workbench

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Two independent sign-ins may resolve to the same (manual-only) account.
// Neither a global draft nor the pool scheduler should capture the other tab.
func TestCodeLoginReturnsToEachInitiatingTerminal(t *testing.T) {
	s := testServer(t)
	home := t.TempDir()
	project, _ := filepath.EvalSymlinks(t.TempDir())
	s.ready = true
	s.preferences.Workspace = project
	if err := writePrivate(filepath.Join(s.profile, "environment.json"), map[string]string{"directory": project}); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 24)
	list := claudeAccountList{Accounts: []claudeAccount{{ID: id, Email: "existing@example.org", Source: "managed", Status: "ready", Enabled: false}}}
	if err := s.saveClaudeAccounts(list); err != nil {
		t.Fatal(err)
	}
	s.claudeAccounts.probe = func(_ context.Context, guest string) (claudeGuestSnapshot, error) {
		return claudeGuestSnapshot{LoggedIn: true, Email: "existing@example.org"}, nil
	}
	mobileTestSSHRewrite(t, s, home, []string{"HOME=" + home, "PATH=/usr/bin:/bin"}, func(command string) string {
		if strings.Contains(command, "cage-claude-account login") {
			return "read confirmation"
		}
		if strings.Contains(command, "exec bash -i") {
			return "exec cat"
		}
		return "echo '{}'"
	})
	t.Cleanup(func() { s.beginClose(); s.terminals.close(); s.activities.Wait() })
	var logins []*terminalSession
	for _, allow := range []bool{false, true} {
		payload, _ := json.Marshal(map[string]any{"kind": "claude", "workspace": project, "claude_account_id": "login", "auto_handoff": allow})
		w := request(s, "POST", "/api/terminals", s.origin, string(payload), true)
		var result struct{ ID, Kind, Workspace string }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Kind != "claude-login" || result.Workspace != project {
			t.Fatalf("login admission %d %s", w.Code, w.Body)
		}
		logins = append(logins, s.terminals.get(result.ID))
	}
	if logins[0].loginGuestID == logins[1].loginGuestID || !validClaudeID(logins[0].loginGuestID) {
		t.Fatal("login drafts were shared")
	}
	for i, login := range logins {
		if err := login.enqueueInput(t.Context(), "confirmed\r"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		var record *mobileRecord
		for time.Now().Before(deadline) {
			next := s.terminals.get(login.id)
			summary := next.summary()
			if next != login && summary["cli_session_id"] != "" {
				s.mobileCLI.mu.Lock()
				record, _ = s.mobileCLI.record(s.profile, summary["cli_session_id"].(string))
				s.mobileCLI.mu.Unlock()
				if record != nil {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		if record == nil {
			t.Fatal("Code did not replace completed login", login.summary())
		}
		want := []string{"wait", "allow"}[i]
		if record.SwitchPolicy != want || record.Account.ID != id || record.Account.GuestID != id || record.Workspace != project {
			t.Fatalf("wrong startup binding or quota policy: %+v", record)
		}
	}
}

func TestObservedLoginPreservesSiblingSession(t *testing.T) {
	s, id, project := metadataFixture(t)
	a, b := strings.Repeat("a", 24), strings.Repeat("b", 24)
	accounts := claudeAccountList{Accounts: []claudeAccount{
		{ID: a, Email: "a@example.org", Source: "managed", Status: "ready", HandoffGroup: "test"},
		{ID: b, Email: "b@example.org", Source: "managed", Status: "ready", HandoffGroup: "test", Enabled: true}}}
	if err := s.saveClaudeAccounts(accounts); err != nil {
		t.Fatal(err)
	}
	s.claudeAccounts.probe = func(_ context.Context, dir string) (claudeGuestSnapshot, error) {
		return claudeGuestSnapshot{LoggedIn: true, Email: "b@example.org"}, nil
	}
	first, _ := s.mobileCLI.record(s.profile, id)
	first.Account = claudeSelection{a, a, claudeIdentityHash("a@example.org", "")}
	first.Connection = "22222222-2222-4222-8222-222222222222"
	first.Launcher = "claude"
	// The first session owns the browser. Both terminals retain their own
	// connection IDs and private histories while observing the same directory.
	other := *first
	other.ID = "00000000-0000-4000-8000-000000000001"
	other.Connection = "11111111-1111-4111-8111-111111111111"
	other.TerminalID = "other"
	for _, rec := range []*mobileRecord{first, &other} {
		if err := writePrivate(mobileRecordPath(s.profile, rec.ID), rec); err != nil {
			t.Fatal(err)
		}
		terminal := newTerminalSession(nil)
		terminal.id, terminal.connectionID, terminal.claudeAccountID = rec.TerminalID, rec.Connection, a
		terminal.closed = true
		close(terminal.done)
		s.terminals.sessions[rec.TerminalID] = terminal
	}
	mobileTestSSHRewrite(t, s, project, []string{"HOME=" + project, "PATH=/usr/bin:/bin"}, func(string) string { return `echo '{"ok":true}'` })
	for _, rec := range []*mobileRecord{&other, first} {
		state := codeLoginObservation{current: codeLoginHint{Stamp: "1", Subject: claudeIdentityHash("b@example.org", ""), Owner: first.Connection}, connection: rec.Connection}
		if !s.observeCodeLogin(t.Context(), rec.snapshot(), &state) {
			t.Fatal("ignored changed identity")
		}
		got, _ := s.mobileCLI.record(s.profile, rec.ID)
		if rec == first {
			if got.Account.ID != b || got.Account.GuestID != a {
				t.Fatal("login owner did not retain its actual directory", got.Account)
			}
		} else if got.Account != rec.Account || !strings.Contains(s.terminals.get(rec.TerminalID).summary()["login_notice"].(string), "已停止") {
			t.Fatal("sibling was rebound", got.Account)
		}
	}
}

func TestUnboundCodeLoginNeedsItsOwnRequestWhenSharingProfile(t *testing.T) {
	var manager terminalManager
	first, second := newTerminalSession(nil), newTerminalSession(nil)
	first.kind, second.kind = "claude", "claude"
	manager.sessions = map[string]*terminalSession{"first": first}
	if !manager.ownsLoginRequest(first, "default", true) {
		t.Fatal("a single unbound terminal should detect its initial login")
	}
	manager.sessions["second"] = second
	if manager.ownsLoginRequest(first, "default", true) {
		t.Fatal("ambiguous initial login claimed by a terminal")
	}
	second.loginRequested = time.Now()
	if manager.ownsLoginRequest(first, "default", true) || !manager.ownsLoginRequest(second, "default", true) {
		t.Fatal("new login attributed to the wrong terminal")
	}
}
