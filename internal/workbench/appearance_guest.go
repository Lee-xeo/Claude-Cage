package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
)

type guestThemeState struct {
	Variant string `json:"variant"`
}

func validGuestVariant(variant string) bool {
	return variant == "light" || variant == "dark"
}

func loadSavedAppearance(profile string) (Appearance, bool, error) {
	a := defaultAppearance()
	b, err := os.ReadFile(filepath.Join(profile, "appearance.json"))
	if os.IsNotExist(err) {
		return a, false, nil
	}
	if err != nil {
		return a, false, errors.New("无法读取已保存的外观设置")
	}
	if err = json.Unmarshal(b, &a); err != nil {
		return a, false, errors.New("无法读取已保存的外观设置")
	}
	if err = a.validate(); err != nil {
		return a, false, err
	}
	return a, true, nil
}

func loadGuestThemeState(profile string) (guestThemeState, error) {
	var state guestThemeState
	b, err := os.ReadFile(filepath.Join(profile, "guest-theme.json"))
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return guestThemeState{}, err
	}
	return state, nil
}

func saveGuestThemeState(profile, variant string) error {
	return writePrivate(filepath.Join(profile, "guest-theme.json"), guestThemeState{Variant: variant})
}

func resolvedGuestTheme(profile string) string {
	a, exists, err := loadSavedAppearance(profile)
	if err != nil {
		return ""
	}
	if !exists {
		state, err := loadGuestThemeState(profile)
		if err != nil || !validGuestVariant(state.Variant) {
			return ""
		}
		return state.Variant
	}
	if !a.SyncGuestTheme {
		return ""
	}
	if validGuestVariant(a.Mode) {
		return a.Mode
	}
	state, err := loadGuestThemeState(profile)
	if err != nil || !validGuestVariant(state.Variant) {
		return ""
	}
	return state.Variant
}

func (s *Server) isReady() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready, s.credentials.Token
}

func (s *Server) applyGuestTheme(ctx context.Context, variant string) error {
	if !validGuestVariant(variant) {
		return errors.New("主题模式无效，仅支持 light 或 dark")
	}
	s.guestThemeMu.Lock()
	defer s.guestThemeMu.Unlock()

	ready, token := s.isReady()
	if !ready {
		return nil
	}
	var errs []error
	body, _ := json.Marshal(guestThemeState{Variant: variant})
	if _, err := s.guestAPI(ctx, token, http.MethodPost, "/theme", body); err != nil {
		errs = append(errs, err)
	}
	if s.shouldSyncGuestFiles() {
		script := "if command -v python3 >/dev/null 2>&1; then /usr/bin/python3 -I - " +
			shellescape.Quote(variant) + " <<'CAGE_THEME_PY'\n" +
			cage.GuestThemeScript + "\nCAGE_THEME_PY\nfi"
		if _, err := s.guest(ctx, script); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) shouldSyncGuestFiles() bool {
	if s.profile == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(s.instanceDir(), "ssh.config"))
	return err == nil
}

func (s *Server) clearGuestBrowserTheme(ctx context.Context) error {
	s.guestThemeMu.Lock()
	defer s.guestThemeMu.Unlock()

	ready, token := s.isReady()
	if !ready {
		return nil
	}
	body, _ := json.Marshal(guestThemeState{Variant: ""})
	_, err := s.guestAPI(ctx, token, http.MethodPost, "/theme", body)
	return err
}

func (s *Server) runGuestThemeTask(fn func(context.Context) error) {
	if ready, _ := s.isReady(); !ready {
		return
	}
	ctx, finish, err := s.beginActivity(context.Background())
	if err != nil {
		return
	}
	go func() {
		defer finish()
		taskCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := fn(taskCtx); err != nil && s.profile != "" {
			_ = diagnostics.New(s.profile).Record("warn", "appearance", err.Error())
		}
	}()
}

func (s *Server) applyGuestThemeAsync(variant string) {
	s.runGuestThemeTask(func(ctx context.Context) error {
		return s.applyGuestTheme(ctx, variant)
	})
}

func (s *Server) clearGuestBrowserThemeAsync() {
	s.runGuestThemeTask(s.clearGuestBrowserTheme)
}

func (s *Server) onAppearanceSaved(a Appearance) {
	if !a.SyncGuestTheme {
		s.clearGuestBrowserThemeAsync()
		return
	}
	if validGuestVariant(a.Mode) {
		_ = saveGuestThemeState(s.profile, a.Mode)
		s.applyGuestThemeAsync(a.Mode)
		return
	}
	if st, err := loadGuestThemeState(s.profile); err == nil && validGuestVariant(st.Variant) {
		s.applyGuestThemeAsync(st.Variant)
	}
}

func (s *Server) syncGuestAppearance(w http.ResponseWriter, r *http.Request) {
	var input guestThemeState
	if !readJSON(w, r, &input, 4<<10) {
		return
	}
	if !validGuestVariant(input.Variant) {
		requestError(w, errors.New("主题模式无效，仅支持 light 或 dark"))
		return
	}
	if err := saveGuestThemeState(s.profile, input.Variant); err != nil {
		requestError(w, errors.New("主题同步状态未能保存，请重试"))
		return
	}
	a, _, err := loadSavedAppearance(s.profile)
	if err != nil {
		requestError(w, err)
		return
	}
	if !a.SyncGuestTheme {
		jsonReply(w, map[string]any{"ok": true, "variant": input.Variant, "synced": false})
		return
	}
	synced := false
	if ready, _ := s.isReady(); ready {
		if ctx, finish, err := s.beginActivity(r.Context()); err == nil {
			defer finish()
			taskCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if err = s.applyGuestTheme(taskCtx, input.Variant); err != nil {
				if s.profile != "" {
					_ = diagnostics.New(s.profile).Record("warn", "appearance", err.Error())
				}
			} else {
				synced = true
			}
		}
	}
	jsonReply(w, map[string]any{"ok": true, "variant": input.Variant, "synced": synced})
}
