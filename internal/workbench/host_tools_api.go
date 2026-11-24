package workbench

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
	"github.com/sylvinus/agent-vm/internal/hostproxy"
	"github.com/sylvinus/agent-vm/internal/version"
)

// hostPluginView is one plugin as the plugin page shows it.
type hostPluginView struct {
	Name        string                              `json:"name"`
	DisplayName string                              `json:"display_name,omitempty"`
	Version     string                              `json:"version,omitempty"`
	Description string                              `json:"description,omitempty"`
	I18n        map[string]hostbridge.LocalizedText `json:"i18n,omitempty"`
	Permissions []string                            `json:"permissions"`
	// Categories are the plugin's market sections this application knows.
	Categories []string `json:"categories,omitempty"`
	// Kind is host or guest: where the plugin's programs run.
	Kind        string               `json:"kind,omitempty"`
	Contributes   *hostContributesView `json:"contributes,omitempty"`
	BuiltIn       bool                 `json:"built_in,omitempty"`
	Default       bool                 `json:"default,omitempty"`
	DefaultUpdate string               `json:"default_update,omitempty"`
	// Icon says the plugin has a picture, served by the icon route.
	Icon      bool `json:"icon,omitempty"`
	Supported bool `json:"supported"`
	// Always says calls to the plugin's tools are allowed without asking.
	// AlwaysProjects, for the built-in plugin, are the folders in which
	// commands are.
	Always         bool     `json:"always"`
	AlwaysProjects []string `json:"always_projects,omitempty"`
	// Enabled is false for a plugin the user turned off.
	Enabled bool                    `json:"enabled"`
	Loaded  bool                    `json:"loaded"`
	Tools   []hostbridge.ToolInfo   `json:"tools,omitempty"`
	Prompts []hostbridge.PromptInfo `json:"prompts,omitempty"`
	// Problem is why the plugin's folder cannot be used.
	Problem string `json:"problem,omitempty"`
	// MissingProgram names the host program (such as uv, uvx, npx, node or
	// python3) that was not found on PATH when starting the plugin.
	MissingProgram string `json:"missing_program,omitempty"`
	// Settings are what the user enters for the plugin; NeedsSettings says
	// a required one has no value, so the plugin is not started.
	Settings      []hostSettingView `json:"settings,omitempty"`
	NeedsSettings bool              `json:"needs_settings,omitempty"`
}

// hostContributesView is what a plugin adds, as far as the page shows it.
type hostContributesView struct {
	Views       []hostViewView   `json:"views"`
	URIHandlers []hostSchemeView `json:"uri_handlers"`
	MCPServers  []hostServerView `json:"mcp_servers"`
	DesktopApps []hostAppView    `json:"desktop_apps"`
}

// hostAppView is a desktop application the plugin installs in the
// environment.
type hostAppView struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Icon    bool     `json:"icon"`
	Source  string   `json:"source"`
	Schemes []string `json:"schemes"`
}

type hostViewView struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Icon     bool     `json:"icon"`
	OpenURLs []string `json:"open_urls"`
	// URL is where the page is loaded from. Empty when the plugin cannot be
	// used as it is.
	URL string `json:"url,omitempty"`
}

type hostSchemeView struct {
	Scheme string `json:"scheme"`
}

type hostServerView struct {
	ID string `json:"id"`
}

func hostContributes(c *hostbridge.Contributes) *hostContributesView {
	if c == nil {
		return nil
	}
	view := &hostContributesView{Views: []hostViewView{}, URIHandlers: []hostSchemeView{}, MCPServers: []hostServerView{}, DesktopApps: []hostAppView{}}
	for _, page := range c.Views {
		urls := page.OpenURLs
		if urls == nil {
			urls = []string{}
		}
		view.Views = append(view.Views, hostViewView{ID: page.ID, Title: page.Title, Icon: page.Icon != "", OpenURLs: urls})
	}
	for _, handler := range c.URIHandlers {
		view.URIHandlers = append(view.URIHandlers, hostSchemeView{Scheme: handler.Scheme})
	}
	for _, server := range c.MCPServers {
		view.MCPServers = append(view.MCPServers, hostServerView{ID: server.ID})
	}
	for _, app := range c.DesktopApps {
		schemes := app.Schemes
		if schemes == nil {
			schemes = []string{}
		}
		view.DesktopApps = append(view.DesktopApps, hostAppView{ID: app.ID, Title: app.Title, Icon: app.Icon != "", Source: app.Source.Type, Schemes: schemes})
	}
	return view
}

// hostPluginUnavailable says, for the user, why a plugin is not started.
func hostPluginUnavailable(reason string) string {
	switch reason {
	case hostbridge.NeedsNewerApplication:
		return "需要更新 Cage 才能使用这个插件"
	case hostbridge.GuestNotSupported:
		return "这类插件暂不支持，需要更新 Cage"
	case hostbridge.TooManyServers:
		return "暂只支持一个 MCP 服务"
	}
	return ""
}

type hostToolsView struct {
	// Available is false where the tools cannot run at all.
	Available bool `json:"available"`
	Enabled   bool `json:"enabled"`
	Running   bool `json:"running"`
	Headless  bool `json:"headless,omitempty"`
	Platform  string `json:"platform,omitempty"`
	// Directory is where the user puts plugin folders.
	Directory    string                 `json:"directory"`
	Folders      []string               `json:"folders"`
	Plugins      []hostPluginView       `json:"plugins"`
	ClientBridge []hostClientBridgeView `json:"client_bridge,omitempty"`
	Problems     []string               `json:"problems,omitempty"`
	// Catalog is always empty; kept for windows that still read it.
	Catalog []hostCatalogView `json:"catalog"`
	// DismissedDefaults lists bundled default plugins the user uninstalled and
	// can restore with one click.
	DismissedDefaults []hostDefaultPluginView `json:"dismissed_defaults,omitempty"`
}

type hostCatalogView struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
	Supported   bool     `json:"supported"`
	Installed   bool     `json:"installed"`
}

func (s *Server) hostPluginDir() string { return filepath.Join(s.profile, "plugins") }

func (s *Server) hostToolsView() hostToolsView {
	s.mu.Lock()
	enabled, tools, off := s.preferences.HostTools, s.hostTools, hostPluginsOff(s.preferences)
	var pluginProblems, missingPrograms map[string]string
	var problems []string
	if tools != nil {
		// Replaced, not changed, when a plugin restarts for a new project.
		pluginProblems, missingPrograms, problems = tools.pluginProblems, tools.missingPrograms, tools.problems
	}
	s.mu.Unlock()
	view := hostToolsView{
		Available:    !s.clientOnly,
		Enabled:      enabled,
		Running:      tools != nil && tools.serving,
		Headless:     s.hostIsHeadless(),
		Platform:     runtime.GOOS,
		Directory:    s.hostPluginDir(),
		Folders:      s.hostToolDirs(),
		ClientBridge: s.clientBridge.views(),
	}
	if view.Folders == nil {
		view.Folders = []string{}
	}
	loaded := map[string][]hostbridge.ToolInfo{}
	loadedPrompts := map[string][]hostbridge.PromptInfo{}
	s.pluginSettingsMu.Lock()
	settings := s.readPluginSettings()
	s.pluginSettingsMu.Unlock()
	if tools != nil {
		view.Problems = problems
		for _, plugin := range tools.plugins {
			// One waiting for a project folder is kept, not running.
			if plugin.Running() {
				loaded[plugin.Manifest.Name] = plugin.ToolList()
				loadedPrompts[plugin.Manifest.Name] = plugin.PromptList()
			}
		}
	}
	view.Plugins = []hostPluginView{{Name: hostCommandsPlugin, BuiltIn: true, Supported: true, Enabled: !slices.Contains(off, hostCommandsPlugin),
		AlwaysProjects: s.hostGrantStore().projects(),
		Loaded:         tools != nil && tools.serving && !slices.Contains(off, hostCommandsPlugin), Permissions: []string{"commands"},
		Tools:   []hostbridge.ToolInfo{{Name: "run_command"}, {Name: "command_job"}},
		Prompts: (hostbridge.Command{}).PromptList()}}
	entries, _ := os.ReadDir(view.Directory)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var installedNames []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		manifest, err := hostbridge.LoadManifest(filepath.Join(view.Directory, entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			view.Plugins = append(view.Plugins, hostPluginView{Name: entry.Name(), Permissions: []string{}, Problem: err.Error()})
			continue
		}
		unavailable := manifest.Unavailable(version.Version, true)
		_, isDefault := hostbridge.DefaultPluginByName(manifest.Name)
		plugin := hostPluginView{Name: manifest.Name, DisplayName: manifest.DisplayName, Version: manifest.Version, Description: manifest.Description,
			I18n:        hostbridge.LoadI18n(filepath.Join(view.Directory, entry.Name())),
			Categories:  hostbridge.ShownCategories(manifest.Categories),
			Permissions: manifest.Permissions, Kind: manifest.Kind, Contributes: hostContributes(manifest.Contributes),
			Default: isDefault, DefaultUpdate: s.defaultPluginUpdateVersion(manifest.Name, manifest.Version),
			Supported: manifest.Supported() && unavailable == "", Icon: manifest.Icon != "", Enabled: !slices.Contains(off, entry.Name())}
		if manifest.Name == entry.Name() {
			installedNames = append(installedNames, manifest.Name)
			plugin.Settings = pluginSettingsView(manifest, settings[manifest.Name])
			plugin.NeedsSettings = len(pluginMissingSettings(manifest, settings[manifest.Name])) > 0
		}
		// A page of a plugin on this computer has nothing to call while the
		// tools are turned off; one inside the environment still does.
		if plugin.Supported && plugin.Enabled && manifest.Name == entry.Name() && plugin.Contributes != nil &&
			(enabled || manifest.Kind == hostbridge.KindGuest) {
			for index := range plugin.Contributes.Views {
				plugin.Contributes.Views[index].URL = s.hostViewURL(manifest.Name, plugin.Contributes.Views[index].ID)
			}
		}
		// A plugin inside the environment that declares nothing is never
		// asked about.
		plugin.Always = manifest.Kind == hostbridge.KindGuest && len(manifest.Permissions) == 0 ||
			s.hostGrantStore().allowed(hostGrantPlugin+manifest.Name, manifest.Permissions)
		if manifest.Name != entry.Name() {
			plugin.Problem = "文件夹名称须与插件名称 " + manifest.Name + " 一致"
			plugin.Name = entry.Name()
		} else if unavailable != "" {
			plugin.Problem = hostPluginUnavailable(unavailable)
		} else if list, ok := loaded[manifest.Name]; ok {
			plugin.Loaded, plugin.Tools, plugin.Prompts = true, list, loadedPrompts[manifest.Name]
		} else if msg := pluginProblems[manifest.Name]; msg != "" {
			plugin.Problem = msg
			plugin.MissingProgram = missingPrograms[manifest.Name]
		}
		view.Plugins = append(view.Plugins, plugin)
	}
	// The market is the one place plugins are offered: a copy shipped with
	// the application would fall behind it and be installed in its place.
	view.Catalog = []hostCatalogView{}
	view.DismissedDefaults = s.dismissedDefaultPluginViews(installedNames)
	return view
}

// removeHostPlugin sets the plugin's folder aside under plugins-removed, so
// nothing the user put there is lost.
func (s *Server) removeHostPlugin(name string) error {
	_, err := s.setAsideHostPlugin(name)
	return err
}

// setAsideHostPlugin moves the plugin's folder away and says where to.
func (s *Server) setAsideHostPlugin(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", errors.New("插件名称无效")
	}
	source := filepath.Join(s.hostPluginDir(), name)
	if info, err := os.Lstat(source); err != nil || !info.IsDir() {
		return "", errors.New("没有找到这个插件")
	}
	aside := filepath.Join(s.profile, "plugins-removed")
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(aside, name+"-"+time.Now().Format("20060102-150405"))
	return target, os.Rename(source, target)
}

// setHostPluginOn records whether the plugin is started with the others. It
// takes effect when the tools are next started.
func (s *Server) setHostPluginOn(name string, on bool) error {
	if name != hostCommandsPlugin {
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			return errors.New("插件名称无效")
		}
		if info, err := os.Lstat(filepath.Join(s.hostPluginDir(), name)); err != nil || !info.IsDir() {
			return errors.New("没有找到这个插件")
		}
	}
	return s.recordHostPluginOn(name, on)
}

// hostToolProject finds dir among the project folders the commands may run
// in, given as listed in folders or with links resolved, and returns it as
// the commands name it: with links resolved.
func (s *Server) hostToolProject(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	for _, allowed := range s.hostToolDirs() {
		if real, err := filepath.EvalSymlinks(allowed); err == nil && (allowed == dir || real == dir) {
			return real, true
		}
	}
	return "", false
}

// setHostPluginAlways records that the plugin's tools need no asking, or
// takes that back. A grant taken back holds for calls already allowed in
// this start too, so the tools are started again.
func (s *Server) setHostPluginAlways(name, project string, always bool) error {
	grants := s.hostGrantStore()
	if name == hostCommandsPlugin {
		if always {
			// Commands are allowed for good one project at a time, here as in
			// the dialog; Windows's dialog has no such choice, so this is the
			// way there.
			real, ok := s.hostToolProject(project)
			if !ok {
				return errors.New("请选择一个已打开的项目")
			}
			return grants.grant(hostGrantProject+real, []string{"commands"})
		}
		var err error
		if project == "" {
			err = grants.revokeProjects()
		} else {
			err = grants.revoke(hostGrantProject + project)
		}
		if err != nil {
			return err
		}
		s.closeHostTools()
		return nil
	}
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return errors.New("插件名称无效")
	}
	if !always {
		if err := grants.revokePlugin(name); err != nil {
			return err
		}
		s.closeHostTools()
		return nil
	}
	manifest, err := hostbridge.LoadManifest(filepath.Join(s.hostPluginDir(), name))
	if err != nil || manifest.Name != name {
		return errors.New("没有找到这个插件")
	}
	return grants.grant(hostGrantPlugin+name, manifest.Permissions)
}

func (s *Server) recordHostPluginOn(name string, on bool) error {
	s.mu.Lock()
	next := s.preferences
	s.mu.Unlock()
	off := hostPluginsOff(next)
	if on && !slices.Contains(off, name) {
		return nil
	}
	off = slices.DeleteFunc(off, func(other string) bool { return other == name })
	if !on {
		off = append(off, name)
		sort.Strings(off)
	}
	next.HostPluginsOff = strings.Join(off, ",")
	if err := writePrivate(filepath.Join(s.profile, "workbench.json"), next); err != nil {
		return err
	}
	s.mu.Lock()
	s.preferences.HostPluginsOff = next.HostPluginsOff
	s.mu.Unlock()
	return nil
}

func (s *Server) getHostTools(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, s.hostToolsView())
}

// setHostTools turns the tools on or off, installs or removes a plugin, or
// loads the plugin folders again.
// Only this computer's own window may: the tools act on this computer.
func (s *Server) setHostTools(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool  `json:"enabled"`
		Reload  bool   `json:"reload"`
		Install        string `json:"install"`
		RestoreDefault string `json:"restore_default"`
		Remove         string `json:"remove"`
		// Install names a plugin on the community list; Replace sets an
		// installed version aside first. Source is accepted and ignored.
		Source  string `json:"source"`
		Replace bool   `json:"replace"`
		// Plugin and On turn one plugin on or off; the built-in one is
		// named "commands".
		Plugin string `json:"plugin"`
		On     *bool  `json:"on"`
		// Always, with Plugin, allows that plugin's tools without asking, or
		// takes that back. For "commands" it can only be taken back: for
		// every project, or for the one in Project.
		Always  *bool  `json:"always"`
		Project string `json:"project"`
		// Settings, with Plugin, are values to keep for the plugin, by
		// setting id; null clears one. ConfirmGuestSecrets is the user's
		// agreement that a plugin inside the environment gets its secrets.
		Settings            map[string]*string `json:"settings"`
		ConfirmGuestSecrets bool               `json:"confirm_guest_secrets"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.hasPluginControlView(r) {
		http.Error(w, "请在受信任的 Cage 窗口更改主机工具", http.StatusForbidden)
		return
	}
	if s.clientOnly {
		http.Error(w, "此环境不支持主机工具", http.StatusConflict)
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "环境正在执行其他操作，请稍后再试", http.StatusConflict)
		return
	}
	defer s.op.Unlock()
	s.mu.Lock()
	previous, ready, closing := s.preferences, s.ready, s.closing
	s.mu.Unlock()
	if closing {
		http.Error(w, "服务正在关闭", http.StatusServiceUnavailable)
		return
	}
	if body.Enabled != nil && *body.Enabled != previous.HostTools {
		next := previous
		next.HostTools = *body.Enabled
		if err := writePrivate(filepath.Join(s.profile, "workbench.json"), next); err != nil {
			requestError(w, err)
			return
		}
		s.mu.Lock()
		s.preferences.HostTools = next.HostTools
		s.mu.Unlock()
	}
	if body.Plugin != "" && body.Always != nil {
		if err := s.setHostPluginAlways(body.Plugin, body.Project, *body.Always); err != nil {
			requestError(w, err)
			return
		}
	}
	if body.Plugin != "" && body.On != nil {
		if err := s.setHostPluginOn(body.Plugin, *body.On); err != nil {
			requestError(w, err)
			return
		}
	}
	if body.Plugin != "" && body.Settings != nil {
		if err := s.savePluginSettings(body.Plugin, body.Settings, body.ConfirmGuestSecrets); err != nil {
			requestError(w, err)
			return
		}
	}
	if body.Install != "" {
		if err := s.installFromMarket(r.Context(), body.Install, body.Replace); err != nil {
			requestError(w, errors.New("插件安装失败："+err.Error()))
			return
		}
	}
	if body.RestoreDefault != "" {
		if err := s.restoreDefaultPlugin(body.RestoreDefault, body.Replace); err != nil {
			requestError(w, errors.New("默认插件安装失败："+err.Error()))
			return
		}
	}
	if body.Remove != "" {
		// Stop the plugin's program before its folder moves.
		s.closeHostTools()
		if err := s.removeHostPlugin(body.Remove); err != nil {
			requestError(w, err)
			return
		}
		// A plugin installed again later starts turned on, and asks again.
		_ = s.recordHostPluginOn(body.Remove, true)
		_ = s.hostGrantStore().revokePlugin(body.Remove)
		s.forgetPluginSettings(body.Remove)
		s.markDefaultPluginDismissed(body.Remove, true)
	}
	// New settings reach a program only when it starts again.
	if body.Reload || body.Install != "" || body.RestoreDefault != "" || body.Plugin != "" && (body.On != nil || body.Settings != nil) {
		s.closeHostTools()
	}
	if ready || !s.hostToolsView().Enabled {
		s.ensureHostTools()
	}
	jsonReply(w, s.hostToolsView())
}

// hostMarketCache keeps the community list between requests.
type hostMarketCache struct {
	mu      sync.Mutex
	entries []hostbridge.MarketEntry
	fetched time.Time
	err     error
}

const hostMarketMaxAge = 5 * time.Minute

// hostMarketClient reaches the list the way the user's own browser would.
func hostMarketClient() *http.Client {
	return &http.Client{Transport: hostproxy.NewTransport(), Timeout: 2 * time.Minute}
}

// hostMarketEntries reads the community list, at most once per max age
// unless refresh is set. A failed read keeps the last good list.
func (s *Server) hostMarketEntries(ctx context.Context, refresh bool) ([]hostbridge.MarketEntry, time.Time, error) {
	cache := &s.hostMarket
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !refresh && !cache.fetched.IsZero() && time.Since(cache.fetched) < hostMarketMaxAge {
		return cache.entries, cache.fetched, cache.err
	}
	fetch, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entries, err := hostbridge.CommunityMarket.Fetch(fetch, hostMarketClient())
	cache.err = err
	if err == nil {
		cache.entries, cache.fetched = entries, time.Now()
	} else if cache.fetched.IsZero() {
		// Do not ask again on every poll while the list cannot be reached.
		return nil, time.Time{}, err
	}
	return cache.entries, cache.fetched, cache.err
}

type hostMarketEntryView struct {
	Name        string                              `json:"name"`
	DisplayName string                              `json:"display_name,omitempty"`
	Version     string                              `json:"version"`
	Description string                              `json:"description,omitempty"`
	Author      *hostbridge.MarketAuthor            `json:"author,omitempty"`
	I18n        map[string]hostbridge.LocalizedText `json:"i18n,omitempty"`
	Permissions []string                            `json:"permissions"`
	Categories  []string                 `json:"categories,omitempty"`
	Kind        string                   `json:"kind,omitempty"`
	Contributes *hostContributesView     `json:"contributes,omitempty"`
	// Problem is why the plugin cannot be used with this application.
	Problem   string `json:"problem,omitempty"`
	Supported bool   `json:"supported"`
	Bytes     int64  `json:"bytes"`
	// InstalledVersion is empty when the plugin is not installed.
	InstalledVersion string `json:"installed_version"`
	Update           bool   `json:"update"`
	// Icon says the list has a picture for the plugin.
	Icon bool `json:"icon,omitempty"`
	// Runtime names the runtime program the plugin uses, if the list says.
	Runtime string `json:"runtime,omitempty"`
	// Platforms lists the host operating systems the plugin supports.
	Platforms []string `json:"platforms,omitempty"`
	// Source is the open-source project the plugin is built on.
	Source *hostbridge.Source `json:"source,omitempty"`
}

const (
	hostPluginIconPath = "/api/host-tools/icon"
	hostPluginIconCSP  = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
)

// hostPluginIcon serves a plugin's picture: of the installed plugin, or with
// market=1 the one the community list names. Pictures come from plugin
// authors, so they are served as pictures only, with nothing able to run.
func (s *Server) hostPluginIcon(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	var data []byte
	var kind string
	var err error
	if r.URL.Query().Get("market") == "1" {
		entries, _, _ := s.hostMarketEntries(r.Context(), false)
		index := slices.IndexFunc(entries, func(entry hostbridge.MarketEntry) bool { return entry.Name == name })
		if index < 0 {
			http.NotFound(w, r)
			return
		}
		fetch, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		data, kind, err = hostbridge.CommunityMarket.FetchIcon(fetch, hostMarketClient(), entries[index])
	} else {
		data, kind, err = hostbridge.ReadIcon(filepath.Join(s.hostPluginDir(), name))
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", hostPluginIconCSP)
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}

func (s *Server) getHostMarket(w http.ResponseWriter, r *http.Request) {
	entries, fetched, err := s.hostMarketEntries(r.Context(), r.URL.Query().Get("refresh") == "1")
	view := struct {
		Source  string `json:"source"`
		Fetched string `json:"fetched"`
		Error   string `json:"error,omitempty"`
		// Categories are the market's sections, in the order to show them.
		Categories []string              `json:"categories"`
		Entries    []hostMarketEntryView `json:"entries"`
	}{Source: "https://github.com/Lee-xeo/cage-plugins", Categories: hostbridge.KnownCategories, Entries: []hostMarketEntryView{}}
	if !fetched.IsZero() {
		view.Fetched = fetched.UTC().Format(time.RFC3339)
	}
	if err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("market error=%v", err))
		view.Error = "暂时无法读取插件市场，请检查主机网络或代理后重试"
	}
	for _, entry := range entries {
		unavailable := hostbridge.Manifest{Kind: entry.Kind, Engines: entry.Engines, Contributes: entry.Contributes}.Unavailable(version.Version, true)
		platforms := entry.AllPlatforms
		if len(platforms) == 0 {
			platforms = []string{"darwin", "windows", "linux"}
		}
		item := hostMarketEntryView{Name: entry.Name, DisplayName: entry.DisplayName, Version: entry.Version, Description: entry.Description, Author: entry.Author, I18n: entry.I18n,
			Permissions: entry.Permissions, Categories: hostbridge.ShownCategories(entry.Categories), Kind: entry.Kind, Contributes: hostContributes(entry.Contributes), Problem: hostPluginUnavailable(unavailable),
			Supported: entry.Supported() && unavailable == "", Bytes: entry.Bytes, Icon: entry.Icon != "",
			Runtime: entry.Runtime, Platforms: platforms, Source: entry.Source}
		if manifest, err := hostbridge.LoadManifest(filepath.Join(s.hostPluginDir(), entry.Name)); err == nil {
			item.InstalledVersion = manifest.Version
			item.Update = manifest.Version != entry.Version
		} else if _, statErr := os.Lstat(filepath.Join(s.hostPluginDir(), entry.Name)); statErr == nil {
			// A folder of that name that is not a usable plugin.
			item.InstalledVersion, item.Update = "?", true
		}
		view.Entries = append(view.Entries, item)
	}
	if err == nil && len(entries) > 0 {
		s.refreshGuestMarketCatalogAsync()
	}
	jsonReply(w, view)
}

// installFromMarket installs the named plugin from the community list. With
// replace, a version already installed is set aside first and put back if the
// new one cannot be installed.
func (s *Server) installFromMarket(ctx context.Context, name string, replace bool) error {
	entries, _, err := s.hostMarketEntries(ctx, false)
	if err != nil && len(entries) == 0 {
		return err
	}
	index := slices.IndexFunc(entries, func(entry hostbridge.MarketEntry) bool { return entry.Name == name })
	if index < 0 {
		return errors.New("插件市场里没有这个插件")
	}
	if !entries[index].Supported() {
		return errors.New("这个插件不支持此系统")
	}
	if !entries[index].EngineSatisfied(version.Version) {
		return errors.New("需要更新 Cage 才能安装这个插件")
	}
	aside := ""
	if _, statErr := os.Lstat(filepath.Join(s.hostPluginDir(), name)); statErr == nil {
		if !replace {
			return errors.New("这个插件已经安装")
		}
		// Stop the plugin's program before its folder moves.
		s.closeHostTools()
		if aside, err = s.setAsideHostPlugin(name); err != nil {
			return err
		}
	}
	if err = hostbridge.CommunityMarket.Install(ctx, hostMarketClient(), entries[index], s.hostPluginDir()); err != nil && aside != "" {
		if restore := os.Rename(aside, filepath.Join(s.hostPluginDir(), name)); restore != nil {
			return errors.Join(err, restore)
		}
	}
	return err
}
