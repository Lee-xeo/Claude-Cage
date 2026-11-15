package workbench

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
)

func TestProjectShareOptionsCacheNamesAndAttributes(t *testing.T) {
	off, on := projectShareOptions(false), projectShareOptions(true)
	// sshfs's own cache stays off in both: nothing can drop it on request.
	if !strings.Contains(off, "cache=no") || strings.Contains(off, "timeout") {
		t.Fatalf("uncached share: %q", off)
	}
	if on != off+",entry_timeout=20,attr_timeout=20" {
		t.Fatalf("cached share: %q", on)
	}
}

func TestProjectFileCacheChoiceSurvivesOlderClients(t *testing.T) {
	s := testServer(t)
	cfg, err := cage.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	s.preferences.ProjectFileCache = true
	var payload map[string]any
	settings := request(s, "GET", "/api/settings", "", "", true)
	if err = json.Unmarshal(settings.Body.Bytes(), &payload); err != nil || payload["project_file_cache"] != true {
		t.Fatalf("setting unavailable: %v", payload["project_file_cache"])
	}
	p := s.preferences
	p.ProjectFileCache = false
	if err = s.apply(savedProfile{cfg, p}, true); err != nil {
		t.Fatal(err)
	}
	// A client that omits the preference must preserve the user's choice.
	delete(payload, "project_file_cache")
	body, _ := json.Marshal(payload)
	r := httptest.NewRequest("POST", "/api/settings", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	next, ok := s.proposed(httptest.NewRecorder(), r)
	if !ok || next.Preferences.ProjectFileCache {
		t.Fatal("a settings save without the preference turned the cache back on")
	}
}

func TestRefreshProjectFilesNeedsARunningLinuxGuest(t *testing.T) {
	s := testServer(t)
	if w := request(s, "POST", "/api/projects/refresh", s.origin, "{}", true); w.Code != 409 {
		t.Fatalf("stopped environment: %d %s", w.Code, w.Body)
	}
}
