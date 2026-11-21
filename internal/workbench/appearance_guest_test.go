package workbench

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAppearanceLegacySyncGuestThemeAndResolution(t *testing.T) {
	profile := t.TempDir()
	if got := resolvedGuestTheme(profile); got != "" {
		t.Fatalf("empty profile resolved %q, want empty", got)
	}
	legacy := `{
  "mode": "dark",
  "lightTheme": {"preset": "codex", "accent": "#339cff", "surface": "#ffffff", "ink": "#1a1c1f", "contrast": 45},
  "darkTheme": {"preset": "codex", "accent": "#339cff", "surface": "#181818", "ink": "#ffffff", "contrast": 60},
  "uiFontSize": 14,
  "codeFontSize": 13,
  "pointerCursors": false,
  "fontSmoothing": true,
  "reducedMotion": "system"
}`
	if err := os.WriteFile(filepath.Join(profile, "appearance.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{profile: profile}
	w := httptest.NewRecorder()
	s.getAppearance(w, httptest.NewRequest("GET", "/api/appearance", nil))
	if w.Code != 200 {
		t.Fatalf("GET /api/appearance: %d %s", w.Code, w.Body.String())
	}
	var loaded Appearance
	if err := json.Unmarshal(w.Body.Bytes(), &loaded); err != nil || !loaded.SyncGuestTheme {
		t.Fatalf("legacy appearance lost syncGuestTheme default: %+v err=%v", loaded, err)
	}
	if got := resolvedGuestTheme(profile); got != "dark" {
		t.Fatalf("dark appearance resolved %q", got)
	}
	if err := saveGuestThemeState(profile, "light"); err != nil {
		t.Fatal(err)
	}
	// System mode reads resolved light/dark variant from guest-theme.json.
	loaded.Mode = "system"
	if err := writePrivate(filepath.Join(profile, "appearance.json"), loaded); err != nil {
		t.Fatal(err)
	}
	if got := resolvedGuestTheme(profile); got != "light" {
		t.Fatalf("system mode resolved %q, want light", got)
	}
	// Turning off syncGuestTheme clears startup/attach theme resolution.
	loaded.SyncGuestTheme = false
	loaded.Mode = "dark"
	if err := writePrivate(filepath.Join(profile, "appearance.json"), loaded); err != nil {
		t.Fatal(err)
	}
	if got := resolvedGuestTheme(profile); got != "" {
		t.Fatalf("disabled sync resolved %q, want empty", got)
	}
}

func TestSyncGuestAppearanceAndBrowserThemeControl(t *testing.T) {
	s := testServer(t)
	s.ready = true
	var mu sync.Mutex
	var calls []string
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		calls = append(calls, r.URL.Path+":"+string(body))
		mu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"ok":true}`))), Header: http.Header{}}, nil
	}))

	if bad := request(s, "POST", "/api/appearance/guest-theme", s.origin, `{"variant":"sepia"}`, true); bad.Code != 400 {
		t.Fatalf("invalid variant accepted: %d %s", bad.Code, bad.Body.String())
	}
	okResp := request(s, "POST", "/api/appearance/guest-theme", s.origin, `{"variant":"dark"}`, true)
	if okResp.Code != 200 {
		t.Fatalf("POST /api/appearance/guest-theme: %d %s", okResp.Code, okResp.Body.String())
	}
	var reply struct {
		OK      bool   `json:"ok"`
		Variant string `json:"variant"`
		Synced  bool   `json:"synced"`
	}
	if err := json.Unmarshal(okResp.Body.Bytes(), &reply); err != nil || !reply.OK || reply.Variant != "dark" || !reply.Synced {
		t.Fatalf("unexpected guest-theme reply: %+v err=%v", reply, err)
	}
	state, err := loadGuestThemeState(s.profile)
	if err != nil || state.Variant != "dark" {
		t.Fatalf("guest-theme.json = %+v, err=%v", state, err)
	}

	// Disabling syncGuestTheme saves the setting and clears browser's forced override.
	disabled := defaultAppearance()
	disabled.SyncGuestTheme = false
	raw, _ := json.Marshal(disabled)
	saveResp := request(s, "POST", "/api/appearance", s.origin, string(raw), true)
	if saveResp.Code != 200 {
		t.Fatalf("POST /api/appearance: %d %s", saveResp.Code, saveResp.Body.String())
	}
	s.activities.Wait()

	// Subsequent /api/appearance/guest-theme calls record state without pushing when disabled.
	noSync := request(s, "POST", "/api/appearance/guest-theme", s.origin, `{"variant":"light"}`, true)
	if noSync.Code != 200 {
		t.Fatalf("disabled guest-theme reply: %d %s", noSync.Code, noSync.Body.String())
	}
	var skipped struct {
		Synced bool `json:"synced"`
	}
	if err := json.Unmarshal(noSync.Body.Bytes(), &skipped); err != nil || skipped.Synced {
		t.Fatalf("expected synced=false when disabled, got %s", noSync.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{`/theme:{"variant":"dark"}`, `/theme:{"variant":""}`}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("browser /theme calls = %v, want %v", calls, want)
	}
}
