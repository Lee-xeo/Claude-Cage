package hostbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Engines is the application version a plugin needs.
type Engines struct {
	// Cage is a range: >=X.Y.Z, or ^X.Y.Z for that version up to the next
	// incompatible one.
	Cage string `json:"cage"`
}

// Contributes lists what a plugin adds.
type Contributes struct {
	MCPServers  []MCPServer  `json:"mcpServers,omitempty"`
	Views       []View       `json:"views,omitempty"`
	URIHandlers []URIHandler `json:"uriHandlers,omitempty"`
}

// MCPServer is a program that speaks MCP on its standard input and output.
type MCPServer struct {
	ID string `json:"id"`
	// Command is the program and its arguments. A relative program path is
	// taken from the plugin's directory.
	Command []string `json:"command"`
}

// View is a page of the plugin's own.
type View struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	// Entry is an HTML file inside the plugin's directory.
	Entry string `json:"entry"`
	// Server is the MCP server the page may call. Empty for a static page.
	Server string `json:"server,omitempty"`
	// OpenURLs are the address patterns the page may ask to have opened.
	OpenURLs []string `json:"openUrls,omitempty"`
}

// URIHandler hands links of one scheme to a tool.
type URIHandler struct {
	Scheme string `json:"scheme"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	// Reveal is the view to show once the link is handled. Optional.
	Reveal string `json:"reveal,omitempty"`
}

// Kinds of plugin: where its programs run.
const (
	KindHost  = "host"
	KindGuest = "guest"
)

var (
	engineRange = regexp.MustCompile(`^(>=|\^)(\d+)\.(\d+)\.(\d+)$`)
	uriScheme   = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)
	// commonSchemes belong to the browser and the system, never to a plugin.
	commonSchemes = []string{"http", "https", "file", "ftp", "mailto", "data", "javascript", "blob", "about", "chrome", "ws", "wss"}
	urlPort       = regexp.MustCompile(`^(\*|\d{1,5})$`)
	hostName      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

func validID(id string) bool { return pluginName.MatchString(id) && len(id) <= 40 }

// validFile reports whether name is a path inside the plugin with one of the
// extensions.
func validFile(name string, extensions ...string) bool {
	if name == "" || len(name) > 200 || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, "\\") || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	return slices.Contains(extensions, strings.ToLower(path.Ext(name)))
}

// validOpenURL reports whether pattern is https to a named host, or http to
// this computer, with a path that may end in *.
func validOpenURL(pattern string) bool {
	scheme, rest, found := strings.Cut(pattern, "://")
	authority, route, hasPath := strings.Cut(rest, "/")
	if !found || !hasPath || strings.ContainsAny(pattern, " \t\r\n?#@\\") {
		return false
	}
	host, port, hasPort := strings.Cut(authority, ":")
	if hasPort && !urlPort.MatchString(port) {
		return false
	}
	switch scheme {
	case "https":
		if !hostName.MatchString(host) {
			return false
		}
	case "http":
		if host != "127.0.0.1" && host != "localhost" {
			return false
		}
	default:
		return false
	}
	return !strings.Contains(strings.TrimSuffix(route, "*"), "*")
}

func parseManifest(data []byte) (Manifest, error) {
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if err := m.check(); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	return m, nil
}

// check validates the manifest and brings the first format, with a single
// command, to the shape of the second.
func (m *Manifest) check() error {
	switch {
	case !pluginName.MatchString(m.Name) || len(m.Name) > 40:
		return fmt.Errorf("name must be lowercase letters, digits and hyphens")
	case m.Version == "":
		return fmt.Errorf("version is missing")
	case strings.TrimSpace(m.Description) == "":
		return fmt.Errorf("description is missing")
	case m.Permissions == nil:
		return fmt.Errorf("permissions must be declared, as an empty list if there are none")
	case len(m.DisplayName) > 80:
		return fmt.Errorf("displayName is too long")
	}
	for _, permission := range m.Permissions {
		if !slices.Contains(KnownPermissions, permission) {
			return fmt.Errorf("unknown permission %q", permission)
		}
	}
	if len(m.Categories) > 3 {
		return fmt.Errorf("at most three categories")
	}
	for i, category := range m.Categories {
		if !validID(category) || slices.Contains(m.Categories[:i], category) {
			return fmt.Errorf("category %q must be lowercase letters, digits and hyphens, and named once", category)
		}
	}
	if m.Icon != "" && !validIcon(m.Icon) {
		return fmt.Errorf("icon must be a PNG or SVG file inside the plugin")
	}
	for _, platform := range m.Platforms {
		if !slices.Contains([]string{"darwin", "linux", "windows"}, platform) {
			return fmt.Errorf("unknown platform %q", platform)
		}
	}
	if m.Contributes == nil {
		switch {
		case len(m.Command) == 0 || m.Command[0] == "":
			return fmt.Errorf("command is missing")
		case m.Kind != "" && m.Kind != KindHost:
			return fmt.Errorf("kind %q needs contributes", m.Kind)
		case m.Engines != nil || len(m.ActivationEvents) > 0:
			return fmt.Errorf("engines and activationEvents need contributes")
		}
		m.Kind = KindHost
		m.Contributes = &Contributes{MCPServers: []MCPServer{{ID: m.Name, Command: m.Command}}}
		return nil
	}
	switch {
	case len(m.Command) > 0:
		return fmt.Errorf("command belongs in contributes.mcpServers")
	case m.Engines == nil || !engineRange.MatchString(m.Engines.Cage):
		return fmt.Errorf("engines.cage must be >=X.Y.Z or ^X.Y.Z")
	case m.Kind != KindHost && m.Kind != KindGuest:
		return fmt.Errorf("kind must be host or guest")
	case m.Kind == KindGuest && len(m.Platforms) > 0:
		return fmt.Errorf("platforms is for host plugins")
	}
	var servers, views, schemes []string
	for _, server := range m.Contributes.MCPServers {
		switch {
		case !validID(server.ID) || slices.Contains(servers, server.ID):
			return fmt.Errorf("mcpServers: id %q is not valid or is repeated", server.ID)
		case len(server.Command) == 0 || server.Command[0] == "":
			return fmt.Errorf("mcpServers %s: command is missing", server.ID)
		}
		servers = append(servers, server.ID)
	}
	for _, view := range m.Contributes.Views {
		switch {
		case !validID(view.ID) || slices.Contains(views, view.ID):
			return fmt.Errorf("views: id %q is not valid or is repeated", view.ID)
		case view.Title == "" || len(view.Title) > 80:
			return fmt.Errorf("views %s: title is missing or too long", view.ID)
		case !validFile(view.Entry, ".html"):
			return fmt.Errorf("views %s: entry must be an HTML file inside the plugin", view.ID)
		case view.Icon != "" && !validIcon(view.Icon):
			return fmt.Errorf("views %s: icon must be a PNG or SVG file inside the plugin", view.ID)
		case view.Server != "" && !slices.Contains(servers, view.Server):
			return fmt.Errorf("views %s: server %q is not one of mcpServers", view.ID, view.Server)
		}
		for _, pattern := range view.OpenURLs {
			if !validOpenURL(pattern) {
				return fmt.Errorf("views %s: openUrls %q must be https to a named host, or http to 127.0.0.1 or localhost, with a path that may end in *", view.ID, pattern)
			}
		}
		views = append(views, view.ID)
	}
	for _, handler := range m.Contributes.URIHandlers {
		switch {
		case !uriScheme.MatchString(handler.Scheme) || len(handler.Scheme) > 40 || slices.Contains(commonSchemes, handler.Scheme):
			return fmt.Errorf("uriHandlers: scheme %q is not valid or is reserved", handler.Scheme)
		case slices.Contains(schemes, handler.Scheme):
			return fmt.Errorf("uriHandlers: scheme %q is repeated", handler.Scheme)
		case !slices.Contains(servers, handler.Server):
			return fmt.Errorf("uriHandlers %s: server %q is not one of mcpServers", handler.Scheme, handler.Server)
		case handler.Tool == "":
			return fmt.Errorf("uriHandlers %s: tool is missing", handler.Scheme)
		case handler.Reveal != "" && !slices.Contains(views, handler.Reveal):
			return fmt.Errorf("uriHandlers %s: reveal %q is not one of views", handler.Scheme, handler.Reveal)
		}
		schemes = append(schemes, handler.Scheme)
	}
	for _, event := range m.ActivationEvents {
		kind, id, _ := strings.Cut(event, ":")
		if !(event == "onStartupFinished" || kind == "onView" && slices.Contains(views, id) || kind == "onUri" && slices.Contains(schemes, id)) {
			return fmt.Errorf("unknown activation event %q", event)
		}
	}
	return nil
}

// EngineSatisfied reports whether the application at version meets the
// plugin's engines range. A manifest without one, and a build without a
// version, always do.
func (m Manifest) EngineSatisfied(version string) bool {
	if m.Engines == nil || version == "" {
		return true
	}
	return engineSatisfied(m.Engines.Cage, version)
}

func engineSatisfied(spec, version string) bool {
	match := engineRange.FindStringSubmatch(spec)
	if match == nil {
		return false
	}
	// A prerelease of a version counts as that version.
	core, _, _ := strings.Cut(version, "-")
	var want, have [3]int
	parts := strings.SplitN(core, ".", 3)
	for i := range want {
		want[i], _ = strconv.Atoi(match[i+2])
		if i < len(parts) {
			have[i], _ = strconv.Atoi(parts[i])
		}
	}
	if slices.Compare(have[:], want[:]) < 0 {
		return false
	}
	if match[1] == ">=" {
		return true
	}
	// ^ keeps the first number that is not zero.
	for i := range want {
		if want[i] != 0 || i == 2 {
			return slices.Equal(have[:i+1], want[:i+1])
		}
	}
	return true
}

// Reasons a plugin that is installed is not started.
const (
	NeedsNewerApplication = "engine"
	GuestNotSupported     = "guest"
	TooManyServers        = "servers"
)

// Unavailable is why this application at version does not start the plugin,
// or empty when it does. guests says whether there is somewhere to run a
// guest plugin.
func (m Manifest) Unavailable(version string, guests bool) string {
	switch {
	case !m.EngineSatisfied(version):
		return NeedsNewerApplication
	case m.Kind == KindGuest && !guests:
		return GuestNotSupported
	case m.Contributes != nil && len(m.Contributes.MCPServers) > 1:
		return TooManyServers
	}
	return ""
}

// Schemes are the link schemes the plugin handles.
func (m Manifest) Schemes() []string {
	if m.Contributes == nil {
		return nil
	}
	var schemes []string
	for _, handler := range m.Contributes.URIHandlers {
		schemes = append(schemes, handler.Scheme)
	}
	return schemes
}

// ServerCommand is the command of the plugin's MCP server, if it has one.
func (m Manifest) ServerCommand() []string { return m.serverCommand() }

func (m Manifest) serverCommand() []string {
	if m.Contributes == nil || len(m.Contributes.MCPServers) == 0 {
		return nil
	}
	return m.Contributes.MCPServers[0].Command
}

// Target names the system and processor a packaged plugin is for, as
// darwin-arm64, linux-x64 or win32-x64. A guest plugin runs on Linux
// whatever the host.
func Target(kind string) string {
	system := map[string]string{"windows": "win32"}[runtime.GOOS]
	if system == "" {
		system = runtime.GOOS
	}
	if kind == KindGuest {
		system = "linux"
	}
	processor := map[string]string{"amd64": "x64"}[runtime.GOARCH]
	if processor == "" {
		processor = runtime.GOARCH
	}
	return system + "-" + processor
}
