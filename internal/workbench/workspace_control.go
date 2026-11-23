package workbench

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

const (
	workspaceControlPlugin          = "use-cage"
	workspaceControlManageTool      = "workspace_manage_extension"
	workspaceControlPreferencesTool = "workspace_update_preferences"
	workspaceControlReloadDelay     = 150 * time.Millisecond
)

// workspaceControlBridge serves a narrow, capability-scoped loopback HTTP API
// exclusively to the workspace-control host plugin. It uses a dedicated
// ephemeral bearer token and exposes ONLY safe status/appearance/preference/
// extension-lifecycle endpoints — never proxy routing, shared folders, grants,
// credentials, or secrets.
type workspaceControlBridge struct {
	owner    *Server
	token    string
	listener net.Listener
	server   *http.Server
	once     sync.Once
}

func (s *Server) startWorkspaceControlBridge() (*workspaceControlBridge, error) {
	token, err := hostbridge.NewToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &workspaceControlBridge{owner: s, token: token, listener: listener}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", b.handleStatus)
	mux.HandleFunc("POST /appearance", b.handleAppearance)
	mux.HandleFunc("POST /preferences", b.handlePreferences)
	mux.HandleFunc("POST /extensions", b.handleExtensions)
	b.server = &http.Server{
		Handler:           b.authorize(mux),
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}

func (b *workspaceControlBridge) URL() string {
	return "http://" + b.listener.Addr().String()
}

func (b *workspaceControlBridge) Token() string { return b.token }

func (b *workspaceControlBridge) Close() {
	if b == nil {
		return
	}
	b.once.Do(func() { _ = b.server.Close() })
}

func (b *workspaceControlBridge) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(b.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeStrictJSON(r *http.Request, dst any, limit int64) error {
	raw, err := readLimitedBody(r, limit)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing JSON content")
	}
	return nil
}

func readLimitedBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("empty request body")
	}
	defer r.Body.Close()
	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 1024)
	for {
		n, err := r.Body.Read(chunk)
		if n > 0 {
			if int64(len(buf)+n) > limit {
				return nil, errors.New("request body too large")
			}
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
	}
	return buf, nil
}

func (s *Server) readSavedAppearance() Appearance {
	a := defaultAppearance()
	if raw, err := os.ReadFile(filepath.Join(s.profile, "appearance.json")); err == nil {
		_ = json.Unmarshal(raw, &a)
	}
	return a
}

type workspaceExtensionSummary struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Version     string `json:"version,omitempty"`
	Enabled     bool   `json:"enabled"`
	Loaded      bool   `json:"loaded"`
	ToolCount   int    `json:"tool_count"`
	Core        bool   `json:"core,omitempty"`
}

func neutralExtensionAlias(name string) string {
	switch name {
	case cagePluginName:
		return cagePluginGuestFolderName
	case workspaceControlPlugin:
		return "workspace-control"
	default:
		return name
	}
}

func resolveExtensionAlias(name string) string {
	switch name {
	case cagePluginGuestFolderName:
		return cagePluginName
	case "workspace-control":
		return workspaceControlPlugin
	default:
		return name
	}
}

func (b *workspaceControlBridge) handleStatus(w http.ResponseWriter, r *http.Request) {
	s := b.owner
	appearance := s.readSavedAppearance()
	s.mu.Lock()
	prefs := s.preferences
	s.mu.Unlock()

	toolsView := s.hostToolsView()
	extensions := make([]workspaceExtensionSummary, 0, len(toolsView.Plugins))
	for _, p := range toolsView.Plugins {
		extensions = append(extensions, workspaceExtensionSummary{
			Name:        neutralExtensionAlias(p.Name),
			DisplayName: neutralCatalogText(p.DisplayName),
			Version:     p.Version,
			Enabled:     p.Enabled,
			Loaded:      p.Loaded,
			ToolCount:   len(p.Tools),
			Core:        p.BuiltIn,
		})
	}
	drafts, _, _ := s.listSubmittedDrafts(r.Context())

	// Return strictly neutral, non-sensitive workspace state only.
	jsonReply(w, map[string]any{
		"appearance": map[string]any{
			"mode":            appearance.Mode,
			"ui_font_size":    appearance.UIFontSize,
			"code_font_size":  appearance.CodeFontSize,
			"pointer_cursors": appearance.PointerCursors,
			"font_smoothing":  appearance.FontSmoothing,
			"reduced_motion":  appearance.ReducedMotion,
			"light_accent":    appearance.Light.Accent,
			"dark_accent":     appearance.Dark.Accent,
			"light_surface":   appearance.Light.Surface,
			"dark_surface":    appearance.Dark.Surface,
			"light_ink":       appearance.Light.Ink,
			"dark_ink":        appearance.Dark.Ink,
		},
		"preferences": map[string]any{
			"balanced_display":        prefs.BalancedDisplay,
			"clock_12_hour":           prefs.Clock12Hour,
			"auto_timezone":           prefs.AutoTimezone,
			"auto_language":           prefs.AutoLanguage,
			"project_file_cache":      prefs.ProjectFileCache,
			"browser_home_url":        browserHomeURL(prefs),
			"claude_terminal_command": claudeTerminalCommand(prefs),
		},
		"project_folders":     toolsView.Folders,
		"extensions":          extensions,
		"pending_draft_count": len(drafts),
	})
}

func (b *workspaceControlBridge) handleAppearance(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		Mode           *string `json:"mode"`
		UIFontSize     *int    `json:"ui_font_size"`
		CodeFontSize   *int    `json:"code_font_size"`
		PointerCursors *bool   `json:"pointer_cursors"`
		FontSmoothing  *bool   `json:"font_smoothing"`
		ReducedMotion  *string `json:"reduced_motion"`
		LightAccent    *string `json:"light_accent"`
		DarkAccent     *string `json:"dark_accent"`
		LightSurface   *string `json:"light_surface"`
		DarkSurface    *string `json:"dark_surface"`
		LightInk       *string `json:"light_ink"`
		DarkInk        *string `json:"dark_ink"`
	}
	if err := decodeStrictJSON(r, &patch, 64<<10); err != nil {
		http.Error(w, "invalid appearance update: "+err.Error(), http.StatusBadRequest)
		return
	}
	s := b.owner
	a := s.readSavedAppearance()
	if patch.Mode != nil {
		a.Mode = *patch.Mode
	}
	if patch.UIFontSize != nil {
		a.UIFontSize = *patch.UIFontSize
	}
	if patch.CodeFontSize != nil {
		a.CodeFontSize = *patch.CodeFontSize
	}
	if patch.PointerCursors != nil {
		a.PointerCursors = *patch.PointerCursors
	}
	if patch.FontSmoothing != nil {
		a.FontSmoothing = *patch.FontSmoothing
	}
	if patch.ReducedMotion != nil {
		a.ReducedMotion = *patch.ReducedMotion
	}
	if patch.LightAccent != nil {
		a.Light.Accent = *patch.LightAccent
	}
	if patch.DarkAccent != nil {
		a.Dark.Accent = *patch.DarkAccent
	}
	if patch.LightSurface != nil {
		a.Light.Surface = *patch.LightSurface
	}
	if patch.DarkSurface != nil {
		a.Dark.Surface = *patch.DarkSurface
	}
	if patch.LightInk != nil {
		a.Light.Ink = *patch.LightInk
	}
	if patch.DarkInk != nil {
		a.Dark.Ink = *patch.DarkInk
	}
	if err := a.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := writePrivate(filepath.Join(s.profile, "appearance.json"), a); err != nil {
		http.Error(w, "failed to save appearance settings", http.StatusInternalServerError)
		return
	}
	jsonReply(w, map[string]any{
		"updated": true,
		"appearance": map[string]any{
			"mode":            a.Mode,
			"ui_font_size":    a.UIFontSize,
			"code_font_size":  a.CodeFontSize,
			"pointer_cursors": a.PointerCursors,
			"font_smoothing":  a.FontSmoothing,
			"reduced_motion":  a.ReducedMotion,
			"light_accent":    a.Light.Accent,
			"dark_accent":     a.Dark.Accent,
			"light_surface":   a.Light.Surface,
			"dark_surface":    a.Dark.Surface,
			"light_ink":       a.Light.Ink,
			"dark_ink":        a.Dark.Ink,
		},
	})
}

func (b *workspaceControlBridge) handlePreferences(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		BalancedDisplay       *bool   `json:"balanced_display"`
		Clock12Hour           *bool   `json:"clock_12_hour"`
		AutoTimezone          *bool   `json:"auto_timezone"`
		AutoLanguage          *bool   `json:"auto_language"`
		ProjectFileCache      *bool   `json:"project_file_cache"`
		BrowserHomeURL        *string `json:"browser_home_url"`
		ClaudeTerminalCommand *string `json:"claude_terminal_command"`
	}
	if err := decodeStrictJSON(r, &patch, 64<<10); err != nil {
		http.Error(w, "invalid preferences update: "+err.Error(), http.StatusBadRequest)
		return
	}
	if patch.BrowserHomeURL != nil {
		raw := strings.TrimSpace(*patch.BrowserHomeURL)
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(raw) > 2048 {
			http.Error(w, "browser_home_url must be a valid http or https URL", http.StatusBadRequest)
			return
		}
		*patch.BrowserHomeURL = raw
	}
	if patch.ClaudeTerminalCommand != nil {
		cmd := strings.TrimSpace(*patch.ClaudeTerminalCommand)
		if cmd == "" || len(cmd) > 200 || strings.ContainsAny(cmd, "\r\n\x00") {
			http.Error(w, "claude_terminal_command must be a single-line command up to 200 characters", http.StatusBadRequest)
			return
		}
		*patch.ClaudeTerminalCommand = cmd
	}

	s := b.owner
	s.mu.Lock()
	next := s.preferences
	if patch.BalancedDisplay != nil {
		next.BalancedDisplay = *patch.BalancedDisplay
	}
	if patch.Clock12Hour != nil {
		next.Clock12Hour = *patch.Clock12Hour
	}
	if patch.AutoTimezone != nil {
		next.AutoTimezone = *patch.AutoTimezone
	}
	if patch.AutoLanguage != nil {
		next.AutoLanguage = *patch.AutoLanguage
	}
	if patch.ProjectFileCache != nil {
		next.ProjectFileCache = *patch.ProjectFileCache
	}
	if patch.BrowserHomeURL != nil {
		next.BrowserHomeURL = *patch.BrowserHomeURL
	}
	if patch.ClaudeTerminalCommand != nil {
		next.ClaudeTerminalCommand = *patch.ClaudeTerminalCommand
	}
	s.mu.Unlock()

	if err := writePrivate(filepath.Join(s.profile, "workbench.json"), next); err != nil {
		http.Error(w, "failed to save preferences", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.preferences = next
	s.mu.Unlock()

	jsonReply(w, map[string]any{
		"updated": true,
		"preferences": map[string]any{
			"balanced_display":        next.BalancedDisplay,
			"clock_12_hour":           next.Clock12Hour,
			"auto_timezone":           next.AutoTimezone,
			"auto_language":           next.AutoLanguage,
			"project_file_cache":      next.ProjectFileCache,
			"browser_home_url":        browserHomeURL(next),
			"claude_terminal_command": claudeTerminalCommand(next),
		},
	})
}

func (b *workspaceControlBridge) handleExtensions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := decodeStrictJSON(r, &body, 16<<10); err != nil {
		http.Error(w, "invalid extension request: "+err.Error(), http.StatusBadRequest)
		return
	}
	s := b.owner
	switch body.Action {
	case "enable", "disable":
		targetName := resolveExtensionAlias(body.Name)
		if targetName == hostCommandsPlugin {
			http.Error(w, "built-in core tools cannot be changed by this action", http.StatusForbidden)
			return
		}
		if !hostbridge.ValidPluginName(targetName) {
			http.Error(w, "invalid extension name", http.StatusBadRequest)
			return
		}
		if err := s.setHostPluginOn(targetName, body.Action == "enable"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.scheduleReload()
		jsonReply(w, map[string]any{"ok": true, "action": body.Action, "name": neutralExtensionAlias(targetName), "reloading": true})
	case "reload":
		b.scheduleReload()
		jsonReply(w, map[string]any{"ok": true, "action": "reload", "reloading": true})
	default:
		http.Error(w, "action must be enable, disable, or reload", http.StatusBadRequest)
	}
}

// scheduleReload restarts host tools shortly after the HTTP response flushes
// so the calling workspace-control MCP process finishes returning its tool
// reply cleanly before being restarted.
func (b *workspaceControlBridge) scheduleReload() {
	s := b.owner
	go func() {
		time.Sleep(workspaceControlReloadDelay)
		if !s.op.TryLock() {
			return
		}
		defer s.op.Unlock()
		s.mu.Lock()
		closing, ready := s.closing, s.ready
		s.mu.Unlock()
		if closing {
			return
		}
		s.closeHostTools()
		if ready || !s.hostToolsView().Enabled {
			s.ensureHostTools()
		}
	}()
}

// workspaceControlPluginEnv injects the ephemeral loopback control endpoint
// ONLY into the workspace-control host plugin process.
func (s *Server) workspaceControlPluginEnv(control *workspaceControlBridge) func(context.Context, hostbridge.Manifest) ([]string, error) {
	return func(ctx context.Context, manifest hostbridge.Manifest) ([]string, error) {
		env, err := s.pluginSettingsEnv(ctx, manifest)
		if err != nil {
			return nil, err
		}
		if control != nil && manifest.Name == workspaceControlPlugin && manifest.Kind == hostbridge.KindHost {
			env = append(env,
				"WORKSPACE_CONTROL_URL="+control.URL(),
				"WORKSPACE_CONTROL_TOKEN="+control.Token(),
			)
		}
		return env, nil
	}
}
