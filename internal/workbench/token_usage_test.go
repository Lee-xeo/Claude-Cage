package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenUsageCollector(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python unavailable")
	}
	cmd := exec.Command(python, "-I", "token_usage_test.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("collector: %v\n%s", err, output)
	}
}

func TestTokenUsagePrivacyPersistenceAndControls(t *testing.T) {
	s := testServer(t)
	s.ready = true
	calls := 0
	s.tokenUsage.probe = func(ctx context.Context, cutoff float64, scan bool) (tokenUsageSnapshot, error) {
		calls++
		if ctx.Err() != nil {
			return tokenUsageSnapshot{}, ctx.Err()
		}
		days := []tokenUsageDay{}
		if scan {
			days = append(days, tokenUsageDay{Date: "2026-11-16", Model: "claude-sonnet-4-6", Source: "default", Input: 10, Output: 20, Read: 30, Write: 40, Requests: 1})
		}
		return tokenUsageSnapshot{Version: 1, UpdatedAt: time.Now().Unix(), Timezone: "UTC", Today: "2026-11-16", Days: days}, nil
	}
	read := request(s, "GET", "/api/token-usage", s.origin, "", true)
	if read.Code != 200 || calls != 0 {
		t.Fatal("passive read started collection", read.Code, calls)
	}
	refresh := request(s, "POST", "/api/token-usage", s.origin, `{"action":"refresh"}`, true)
	if refresh.Code != 200 || calls != 1 || !strings.Contains(refresh.Body.String(), `"cache_read_input_tokens":30`) {
		t.Fatal(refresh.Code, refresh.Body)
	}
	state, err := s.loadTokenUsage()
	if err != nil || state.Snapshot.Days[0].Write != 40 {
		t.Fatal("snapshot not persisted", state, err)
	}
	info, _ := os.Stat(filepath.Join(s.profile, "token-usage.json"))
	if info == nil {
		t.Fatal("missing snapshot")
	}
	disable := request(s, "POST", "/api/token-usage", s.origin, `{"action":"configure","enabled":false}`, true)
	if disable.Code != 200 {
		t.Fatal(disable.Body)
	}
	if request(s, "POST", "/api/token-usage", s.origin, `{"action":"refresh"}`, true).Code == 200 || calls != 1 {
		t.Fatal("disabled collector ran")
	}
	clear := request(s, "POST", "/api/token-usage", s.origin, `{"action":"clear"}`, true)
	if clear.Code != 200 || calls != 2 {
		t.Fatal(clear.Code, clear.Body)
	}
	state, _ = s.loadTokenUsage()
	if state.Enabled || state.Cutoff <= 0 || len(state.Snapshot.Days) != 0 {
		t.Fatal("clear did not preserve disabled state and cutoff", state)
	}
	if request(s, "POST", "/api/token-usage", s.origin, `{"action":"clear"}`, false).Code == 200 {
		t.Fatal("unauthenticated mutation admitted")
	}
	s.account.lock()
	if request(s, "GET", "/api/token-usage", s.origin, "", true).Code != 423 {
		t.Fatal("locked statistics exposed")
	}
}

func TestTokenUsageFailureRetainsSnapshotAndClearCutoff(t *testing.T) {
	s := testServer(t)
	s.ready = true
	before := tokenUsageState{Enabled: true, Snapshot: tokenUsageSnapshot{Version: 1, Days: []tokenUsageDay{{Date: "2026-11-16", Source: "default", Output: 20}}}}
	if err := writePrivate(filepath.Join(s.profile, "token-usage.json"), before); err != nil {
		t.Fatal(err)
	}
	s.tokenUsage.probe = func(context.Context, float64, bool) (tokenUsageSnapshot, error) {
		return tokenUsageSnapshot{}, errors.New("private secret raw log")
	}
	response := request(s, "POST", "/api/token-usage", s.origin, `{"action":"refresh"}`, true)
	if response.Code == 200 || strings.Contains(response.Body.String(), "private secret") {
		t.Fatal("raw failure leaked", response.Body)
	}
	after, _ := s.loadTokenUsage()
	if len(after.Snapshot.Days) != 1 {
		t.Fatal("failure lost snapshot")
	}
	response = request(s, "POST", "/api/token-usage", s.origin, `{"action":"clear"}`, true)
	if response.Code == 200 {
		t.Fatal("failed guest clear reported success")
	}
	after, _ = s.loadTokenUsage()
	if after.Cutoff <= 0 || len(after.Snapshot.Days) != 0 {
		t.Fatal("failed clear lost durable cutoff")
	}
	data, _ := json.Marshal(after)
	if strings.Contains(string(data), "secret") {
		t.Fatal("private error persisted")
	}
}

func TestTokenUsageRejectsInvalidSnapshotAndFitsWindowsCommand(t *testing.T) {
	s := testServer(t)
	s.ready = true
	s.tokenUsage.probe = func(context.Context, float64, bool) (tokenUsageSnapshot, error) {
		return tokenUsageSnapshot{Version: 1, Days: []tokenUsageDay{{Date: "not-a-date", Source: "../secret", Input: -1}}}, nil
	}
	response := request(s, "POST", "/api/token-usage", s.origin, `{"action":"refresh"}`, true)
	if response.Code == 200 {
		t.Fatal("invalid snapshot admitted")
	}
	if len(tokenUsageCommand()) > 16000 {
		t.Fatal("statistics command exceeds SSH Windows budget")
	}
}
