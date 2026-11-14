package workbench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func isolatedGitConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.WriteFile(path, []byte("[user]\n\tname = Cage Test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHostGitConfigurationIsNotManaged(t *testing.T) {
	path := isolatedGitConfig(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := agentTestController(t)
	for _, method := range []string{"GET", "POST"} {
		w := request(s, method, "/api/host/git", s.origin, `{"confirm":true}`, true)
		if w.Code != 404 && w.Code != 405 {
			t.Fatalf("retired %s route: %d %s", method, w.Code, w.Body.String())
		}
	}
	if _, err := AgentCall(context.Background(), s.configPath, "host.git.enable", json.RawMessage(`{"confirm":true}`)); err == nil {
		t.Fatal("retired agent action accepted")
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(original) {
		t.Fatalf("host Git config changed: %q, %v", saved, err)
	}
}
