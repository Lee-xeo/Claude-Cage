package workbench

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
	"github.com/sylvinus/agent-vm/internal/version"
)

// Plugin pages are shown in a frame that may run scripts but is no origin of
// this workbench: it carries no cookie, so its files cannot be served behind
// the session. They are served under a path only the workbench is told.
const hostViewPrefix = "/plugin-views/"

// hostViewCallWait is how long a page's tool call may take.
const hostViewCallWait = 60 * time.Second

// hostViewFileLimit is the largest file served for a page.
const hostViewFileLimit = 20 << 20

var hostViewTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".txt": "text/plain; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".ico": "image/x-icon",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
	".wasm": "application/wasm",
}

// hostViewProfile is the configuration folder the plugins are in. The
// window of the desktop application is served by a process of its own, with
// its own folder; the pages it shows are those of this computer's
// environment, whose folder holds the plugins.
func (s *Server) hostViewProfile() string {
	if s.desktopEntry && s.appUpdateConfigPath != "" {
		return filepath.Dir(s.appUpdateConfigPath)
	}
	return s.profile
}

// hostViewSecret is what the pages' addresses are derived from. It is kept
// in the configuration folder so that the process that hands out an address
// and the one that serves it, which may differ, agree on it. Only the
// process that runs the environment creates it.
func (s *Server) hostViewSecret() []byte {
	s.hostViewMu.Lock()
	defer s.hostViewMu.Unlock()
	if len(s.hostViewKeyData) > 0 {
		return s.hostViewKeyData
	}
	path := filepath.Join(s.hostViewProfile(), "plugin-views.key")
	data, err := os.ReadFile(path)
	if secret := strings.TrimSpace(string(data)); err == nil && len(secret) >= 32 {
		s.hostViewKeyData = []byte(secret)
		return s.hostViewKeyData
	}
	if s.desktopEntry {
		return nil
	}
	secret := randomToken()
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		// For this start only; the desktop window's own process cannot
		// then serve the pages.
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("page key error=%v", err))
	}
	s.hostViewKeyData = []byte(secret)
	return s.hostViewKeyData
}

// hostViewKey is the path segment that stands for the plugin's pages. Empty
// when there is nothing to derive it from, which matches no address.
func (s *Server) hostViewKey(plugin string) string {
	secret := s.hostViewSecret()
	if len(secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("plugin-view\x00" + plugin))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// hostViewPluginsOff are the plugins turned off, read from the folder when
// this process is not the one that keeps the preferences.
func (s *Server) hostViewPluginsOff() []string {
	if !s.desktopEntry {
		s.mu.Lock()
		defer s.mu.Unlock()
		return hostPluginsOff(s.preferences)
	}
	var saved Preferences
	if data, err := os.ReadFile(filepath.Join(s.hostViewProfile(), "workbench.json")); err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	return hostPluginsOff(saved)
}

func (s *Server) hostViewURL(plugin, view string) string {
	return hostViewPrefix + plugin + "/" + s.hostViewKey(plugin) + "/" + view + "/"
}

// hostViewManifest is the manifest of an installed plugin that is turned on
// and that this application can run.
func (s *Server) hostViewManifest(plugin string) (hostbridge.Manifest, bool) {
	if plugin == "" || plugin != filepath.Base(plugin) || strings.HasPrefix(plugin, ".") {
		return hostbridge.Manifest{}, false
	}
	manifest, err := hostbridge.LoadManifest(filepath.Join(s.hostViewProfile(), "plugins", plugin))
	off := s.hostViewPluginsOff()
	if err != nil || manifest.Name != plugin || !manifest.Supported() || manifest.Unavailable(version.Version, true) != "" || slices.Contains(off, plugin) {
		return hostbridge.Manifest{}, false
	}
	return manifest, true
}

func hostView(manifest hostbridge.Manifest, id string) (hostbridge.View, bool) {
	if manifest.Contributes == nil {
		return hostbridge.View{}, false
	}
	for _, view := range manifest.Contributes.Views {
		if view.ID == id {
			return view, true
		}
	}
	return hostbridge.View{}, false
}

// servePluginView serves a file of a plugin's page: the entry for the page's
// own address, and beside it the files in the entry's folder.
func (s *Server) servePluginView(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, hostViewPrefix), "/", 4)
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || len(parts) != 4 {
		http.NotFound(w, r)
		return
	}
	plugin, key, id, rest := parts[0], parts[1], parts[2], parts[3]
	manifest, usable := s.hostViewManifest(plugin)
	view, found := hostView(manifest, id)
	if expected := s.hostViewKey(plugin); !usable || !found || expected == "" || !hmac.Equal([]byte(key), []byte(expected)) {
		http.NotFound(w, r)
		return
	}
	root := filepath.Join(s.hostViewProfile(), "plugins", plugin)
	// Inside the folder of the page's entry, and no part of the way there a
	// link out of it.
	if rest != "" && (rest != path.Clean(rest) || rest == ".." || strings.HasPrefix(rest, "../") || path.IsAbs(rest) || strings.Contains(rest, "\\")) {
		http.NotFound(w, r)
		return
	}
	name := path.Join(path.Dir(view.Entry), rest)
	if rest == "" {
		name = view.Entry
	}
	// Every step a real folder and the last a plain file: a link, a junction
	// or anything else on the way could lead out of the plugin.
	file := root
	steps := strings.Split(name, "/")
	if info, err := os.Lstat(root); err != nil || info.Mode().Type() != os.ModeDir {
		http.NotFound(w, r)
		return
	}
	for index, part := range steps {
		file = filepath.Join(file, part)
		info, err := os.Lstat(file)
		last := index == len(steps)-1
		if err != nil || (!last && info.Mode().Type() != os.ModeDir) || (last && (!info.Mode().IsRegular() || info.Size() > hostViewFileLimit)) {
			http.NotFound(w, r)
			return
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A fixed table: what the system says a type is differs from machine to
	// machine, and a wrong one stops a script or style from loading.
	kind := hostViewTypes[strings.ToLower(path.Ext(name))]
	if kind == "" {
		kind = "application/octet-stream"
	}
	own := s.origin + hostViewPrefix + plugin + "/"
	header := w.Header()
	header.Del("X-Frame-Options")
	header.Set("Content-Security-Policy", "default-src 'none'; script-src "+own+"; style-src "+own+" 'unsafe-inline'; img-src "+own+
		" data:; font-src "+own+"; connect-src 'none'; frame-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors "+s.origin+
		// Never an origin of this workbench, however the page comes to be
		// loaded: not only when the frame around it says so.
		"; sandbox allow-scripts")
	// The page has no origin of its own, so its fonts and module scripts are
	// fetched as from elsewhere. The files are static and carry no session.
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Type", kind)
	header.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// The reasons a page's call fails, as the page is told them.
const (
	hostCallRefused     = -32001
	hostCallNotAllowed  = -32002
	hostCallUnavailable = -32003
	hostCallTimedOut    = -32004
	hostCallFailed      = -32005
)

func hostCallError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}

// callHostTool lists or calls the tools of the MCP server a plugin's page is
// bound to, for that page.
func (s *Server) callHostTool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Plugin    string          `json:"plugin"`
		View      string          `json:"view"`
		Method    string          `json:"method"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if !readJSON(w, r, &body, 1<<20) {
		return
	}
	manifest, usable := s.hostViewManifest(body.Plugin)
	view, found := hostView(manifest, body.View)
	if !usable || !found {
		hostCallError(w, http.StatusForbidden, hostCallNotAllowed, "这个页面不属于已启用的插件")
		return
	}
	s.mu.Lock()
	tools := s.hostTools
	s.mu.Unlock()
	var plugin *hostbridge.External
	if tools != nil {
		for _, loaded := range tools.plugins {
			if loaded.Manifest.Name == body.Plugin {
				plugin = loaded
			}
		}
	}
	if view.Server == "" || plugin == nil {
		hostCallError(w, http.StatusConflict, hostCallUnavailable, "插件没有在运行，或这个页面没有可调用的服务")
		return
	}
	// By what is running, not what the folder says now. A plugin on this
	// computer acts on it; only this computer's window may drive one. A
	// plugin inside the environment acts where any signed-in workbench
	// already does.
	if plugin.Manifest.Kind != hostbridge.KindGuest && !s.hasTrustedView(r) {
		hostCallError(w, http.StatusForbidden, hostCallNotAllowed, "请在本机 Cage 窗口使用这个插件页面")
		return
	}
	switch body.Method {
	case "tools/list":
		list := []map[string]any{}
		for _, tool := range plugin.PageTools() {
			list = append(list, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
		}
		jsonReply(w, map[string]any{"tools": list})
	case "tools/call":
		if len(body.Arguments) > 0 && !json.Valid(body.Arguments) {
			hostCallError(w, http.StatusBadRequest, -32602, "参数无效")
			return
		}
		// The limit is on the tool; the user may take longer to decide.
		result, err := plugin.CallFromView(r.Context(), body.Name, body.Arguments, hostViewCallWait)
		var refused *hostbridge.RefusedError
		switch {
		case err == nil:
			jsonReply(w, result)
		case errors.Is(err, hostbridge.ErrCallTimedOut):
			hostCallError(w, http.StatusGatewayTimeout, hostCallTimedOut, "工具在 60 秒内没有返回结果")
		case errors.As(err, &refused):
			hostCallError(w, http.StatusForbidden, hostCallRefused, "没有获得允许："+refused.Error())
		default:
			hostCallError(w, http.StatusBadGateway, hostCallFailed, "工具执行出错："+err.Error())
		}
	default:
		hostCallError(w, http.StatusBadRequest, -32601, "没有这个方法")
	}
}
