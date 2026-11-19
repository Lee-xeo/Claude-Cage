package workbench

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

// Links of a scheme a plugin handles travel like this: the environment's
// link opener finds the scheme in pluginLinksList and passes the link to the
// browser service, which tells every open workbench window; the first window
// to pass it here hands it to the plugin's tool, and opens the page the
// plugin names to show for it.

// pluginLinksList is the file in the environment that lists, one per line,
// the schemes whose links go to a plugin.
const pluginLinksList = "/usr/local/lib/plugin-links"

// pluginLinksSeen is how many recent link ids are remembered, so a link that
// several windows pass on is handed to the plugin once.
const pluginLinksSeen = 64

// loadedHostPlugin is the plugin of that name among those loaded, if any.
func (s *Server) loadedHostPlugin(name string) *hostbridge.External {
	s.mu.Lock()
	tools := s.hostTools
	s.mu.Unlock()
	if tools == nil {
		return nil
	}
	for _, plugin := range tools.plugins {
		if plugin.Manifest.Name == name {
			return plugin
		}
	}
	return nil
}

// firstPluginLink reports whether id is new, remembering it.
func (s *Server) firstPluginLink(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Contains(s.pluginLinkIDs, id) {
		return false
	}
	s.pluginLinkIDs = append(s.pluginLinkIDs, id)
	if len(s.pluginLinkIDs) > pluginLinksSeen {
		s.pluginLinkIDs = s.pluginLinkIDs[len(s.pluginLinkIDs)-pluginLinksSeen:]
	}
	return true
}

// openPluginLink hands a link opened in the environment to the plugin that
// handles its scheme.
func (s *Server) openPluginLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if !readJSON(w, r, &body, 16<<10) {
		return
	}
	if body.ID == "" || len(body.ID) > 64 || body.URL == "" || len(body.URL) > 4096 {
		hostCallError(w, http.StatusBadRequest, -32602, "链接无效")
		return
	}
	if !s.firstPluginLink(body.ID) {
		jsonReply(w, map[string]any{"duplicate": true})
		return
	}
	s.mu.Lock()
	tools := s.hostTools
	s.mu.Unlock()
	var plugin *hostbridge.External
	if tools != nil {
		for _, loaded := range tools.plugins {
			if _, handles := loaded.Manifest.LinkHandler(body.URL); handles {
				plugin = loaded
				break
			}
		}
	}
	if plugin == nil {
		hostCallError(w, http.StatusNotFound, hostCallUnavailable, "没有正在运行的插件处理这类链接")
		return
	}
	result, handler, err := plugin.OpenLink(r.Context(), body.URL, hostViewCallWait)
	var refused *hostbridge.RefusedError
	var unshowable unshowableRequest
	switch {
	case err == nil:
		jsonReply(w, map[string]any{"plugin": plugin.Manifest.Name, "view": handler.Reveal, "result": result})
	case errors.As(err, &unshowable):
		hostCallError(w, http.StatusForbidden, hostCallNotAllowed, "请求超出限制："+unshowable.Error())
	case errors.Is(err, hostbridge.ErrCallTimedOut):
		hostCallError(w, http.StatusGatewayTimeout, hostCallTimedOut, "插件在 60 秒内没有处理这个链接")
	case errors.As(err, &refused):
		hostCallError(w, http.StatusForbidden, hostCallRefused, "没有获得允许："+refused.Error())
	default:
		hostCallError(w, http.StatusBadGateway, hostCallFailed, "插件处理链接时出错："+err.Error())
	}
}

// pluginSchemes are the link schemes the loaded plugins handle, sorted.
func pluginSchemes(plugins []*hostbridge.External) []string {
	var schemes []string
	for _, plugin := range plugins {
		for _, scheme := range plugin.Manifest.Schemes() {
			if !slices.Contains(schemes, scheme) {
				schemes = append(schemes, scheme)
			}
		}
	}
	slices.Sort(schemes)
	return schemes
}

// pluginLinksScript makes the environment send links of the schemes to the
// workbench: it lists them for the link opener and makes the opener their
// handler, for the user the session runs as and for the browser's. With no
// schemes it takes all of that away. The schemes have been checked against
// the manifest's pattern and need no quoting.
func pluginLinksScript(schemes []string) string {
	if len(schemes) == 0 {
		return "sudo rm -f " + pluginLinksList + " /usr/local/share/applications/plugin-links.desktop\n"
	}
	mime := ""
	for _, scheme := range schemes {
		mime += "x-scheme-handler/" + scheme + ";"
	}
	script := "set -eu\n" +
		"sudo mkdir -p /usr/local/lib /usr/local/share/applications\n" +
		"printf '%s\\n' " + strings.Join(schemes, " ") + " | sudo tee " + pluginLinksList + " >/dev/null\n" +
		"sudo chmod 0644 " + pluginLinksList + "\n" +
		"printf '%s\\n' '[Desktop Entry]' 'Type=Application' 'Name=Links' 'NoDisplay=true' 'Exec=/usr/local/bin/xdg-open %u' 'MimeType=" + mime + "' | sudo tee /usr/local/share/applications/plugin-links.desktop >/dev/null\n" +
		"for user in \"$(id -un)\" cage-browser; do\n" +
		"  id \"$user\" >/dev/null 2>&1 || continue\n"
	for _, scheme := range schemes {
		script += "  sudo -H -u \"$user\" xdg-mime default plugin-links.desktop x-scheme-handler/" + scheme + " || true\n"
	}
	return script + "done\n"
}

// syncPluginLinks brings the environment's list of plugin link schemes in
// line with the plugins loaded, when it differs from what was last written.
func (s *Server) syncPluginLinks(plugins []*hostbridge.External) {
	schemes := pluginSchemes(plugins)
	key := strings.Join(schemes, " ")
	s.mu.Lock()
	written, known := s.pluginLinksWritten, s.pluginLinksKnown
	s.mu.Unlock()
	if known && written == key || !s.vmRunning() {
		return
	}
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), 30*time.Second)
	defer cancel()
	if _, err := s.guest(ctx, pluginLinksScript(schemes)); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("plugin links error=%v", err))
		return
	}
	s.mu.Lock()
	s.pluginLinksWritten, s.pluginLinksKnown = key, true
	s.mu.Unlock()
}

// activateForView starts a plugin that starts on demand when the page it
// names in onView is opened, so the page finds its tools ready.
func (s *Server) activateForView(name, view string) {
	plugin := s.loadedHostPlugin(name)
	if plugin == nil || !plugin.Waiting() || !slices.Contains(plugin.Manifest.ActivationEvents, "onView:"+view) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.lifetimeContext(), 30*time.Second)
		defer cancel()
		if err := plugin.Activate(ctx); err != nil {
			diagnostics.Log("warn", "host-tools", fmt.Sprintf("activate plugin=%s error=%v", name, err))
		}
	}()
}
