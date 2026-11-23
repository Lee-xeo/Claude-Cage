package workbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
	"github.com/sylvinus/agent-vm/internal/version"
)

// defaultPluginsFile records which bundled default plugins were deployed,
// upgraded, or explicitly dismissed (uninstalled) by the user. It belongs to
// the user's personal profile and is preserved across app updates and reinstalls.
const defaultPluginsFile = "default-plugins.json"

type defaultPluginRecord struct {
	InstalledVersion string `json:"installed_version,omitempty"`
	DeployedSHA256   string `json:"deployed_sha256,omitempty"`
	Dismissed        bool   `json:"dismissed,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

type defaultPluginsData struct {
	Version int                            `json:"version"`
	Plugins map[string]defaultPluginRecord `json:"plugins"`
}

// hostDefaultPluginView describes a bundled default plugin that the user
// uninstalled and can restore with one click from the plugins page.
type hostDefaultPluginView struct {
	Name        string                              `json:"name"`
	DisplayName string                              `json:"display_name,omitempty"`
	Version     string                              `json:"version"`
	Description string                              `json:"description,omitempty"`
	I18n        map[string]hostbridge.LocalizedText `json:"i18n,omitempty"`
	Kind        string                              `json:"kind,omitempty"`
	Permissions []string                            `json:"permissions"`
	Categories  []string                            `json:"categories,omitempty"`
	Contributes *hostContributesView                `json:"contributes,omitempty"`
	Supported   bool                                `json:"supported"`
}

func (s *Server) readDefaultPluginsLocked() map[string]defaultPluginRecord {
	records := map[string]defaultPluginRecord{}
	raw, err := os.ReadFile(filepath.Join(s.profile, defaultPluginsFile))
	if err != nil {
		return records
	}
	var parsed defaultPluginsData
	if json.Unmarshal(raw, &parsed) == nil && parsed.Plugins != nil {
		records = parsed.Plugins
	}
	return records
}

// defaultPluginsUnreadableLocked reports a record file that exists but cannot
// be understood. Treating it as empty would reinstall plugins the user
// removed and then write over what the file still holds.
func (s *Server) defaultPluginsUnreadableLocked() bool {
	raw, err := os.ReadFile(filepath.Join(s.profile, defaultPluginsFile))
	if err != nil {
		return !os.IsNotExist(err)
	}
	var parsed defaultPluginsData
	return json.Unmarshal(raw, &parsed) != nil
}

// defaultPluginDismissed reports a bundled plugin the user uninstalled.
func (s *Server) defaultPluginDismissed(name string) bool {
	if _, ok := hostbridge.DefaultPluginByName(name); !ok {
		return false
	}
	s.defaultPluginsMu.Lock()
	defer s.defaultPluginsMu.Unlock()
	return s.readDefaultPluginsLocked()[name].Dismissed
}

func (s *Server) writeDefaultPluginsLocked(records map[string]defaultPluginRecord) error {
	return writePrivate(filepath.Join(s.profile, defaultPluginsFile), defaultPluginsData{
		Version: 1,
		Plugins: records,
	})
}

// ensureDefaultPlugins deploys bundled default plugins that have not been
// dismissed by the user, and seamlessly upgrades unmodified default plugins
// when the application ships a newer version.
func (s *Server) ensureDefaultPlugins() {
	if s.clientOnly || s.profile == "" {
		return
	}
	s.defaultPluginsMu.Lock()
	defer s.defaultPluginsMu.Unlock()

	if s.defaultPluginsUnreadableLocked() {
		diagnostics.Log("warn", "host-tools", "default-plugins.json cannot be read; bundled plugins are left as they are")
		return
	}
	records := s.readDefaultPluginsLocked()
	changed := false
	dir := s.hostPluginDir()
	now := time.Now().UTC().Format(time.RFC3339)

	for _, bundled := range hostbridge.DefaultPlugins() {
		name := bundled.Manifest.Name
		if !bundled.Manifest.Supported() || bundled.Manifest.Unavailable(version.Version, true) != "" {
			continue
		}
		rec := records[name]
		if rec.Dismissed {
			continue
		}
		target := filepath.Join(dir, name)
		_, statErr := os.Lstat(target)
		if os.IsNotExist(statErr) {
			digest, err := hostbridge.InstallDefaultPlugin(name, dir)
			if err != nil {
				diagnostics.Log("warn", "host-tools", fmt.Sprintf("default plugin %s install error=%v", name, err))
				continue
			}
			records[name] = defaultPluginRecord{
				InstalledVersion: bundled.Manifest.Version,
				DeployedSHA256:   digest,
				Dismissed:        false,
				UpdatedAt:        now,
			}
			changed = true
			continue
		}
		if statErr != nil {
			continue
		}

		// Target folder already exists.
		if rec.InstalledVersion == "" {
			// User already had a folder with the same name before default tracking existed;
			// never overwrite it silently, just track it without a deployed digest.
			manifest, err := hostbridge.LoadManifest(target)
			ver := ""
			if err == nil {
				ver = manifest.Version
			}
			records[name] = defaultPluginRecord{
				InstalledVersion: ver,
				Dismissed:        false,
				UpdatedAt:        now,
			}
			changed = true
			continue
		}

		cmp, ok := hostbridge.CompareVersions(bundled.Manifest.Version, rec.InstalledVersion)
		if !ok || cmp <= 0 || rec.DeployedSHA256 == "" {
			continue
		}
		// Only auto-upgrade when the user has not edited the plugin locally.
		currentDigest, err := hostbridge.PluginDigest(target, name)
		if err != nil || currentDigest != rec.DeployedSHA256 {
			continue
		}
		aside, err := s.setAsideHostPlugin(name)
		if err != nil {
			diagnostics.Log("warn", "host-tools", fmt.Sprintf("default plugin %s set-aside error=%v", name, err))
			continue
		}
		digest, err := hostbridge.InstallDefaultPlugin(name, dir)
		if err != nil {
			_ = os.Rename(aside, target)
			diagnostics.Log("warn", "host-tools", fmt.Sprintf("default plugin %s upgrade error=%v", name, err))
			continue
		}
		records[name] = defaultPluginRecord{
			InstalledVersion: bundled.Manifest.Version,
			DeployedSHA256:   digest,
			Dismissed:        false,
			UpdatedAt:        now,
		}
		changed = true
	}

	if changed {
		if err := s.writeDefaultPluginsLocked(records); err != nil {
			diagnostics.Log("warn", "host-tools", fmt.Sprintf("write default-plugins.json error=%v", err))
		}
	}
}

func (s *Server) markDefaultPluginDismissed(name string, dismissed bool) {
	if _, ok := hostbridge.DefaultPluginByName(name); !ok {
		return
	}
	s.defaultPluginsMu.Lock()
	defer s.defaultPluginsMu.Unlock()
	records := s.readDefaultPluginsLocked()
	rec := records[name]
	rec.Dismissed = dismissed
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	records[name] = rec
	if err := s.writeDefaultPluginsLocked(records); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("update default-plugins.json error=%v", err))
	}
}

// restoreDefaultPlugin reinstalls (or updates, with replace=true) a bundled
// default plugin and clears its dismissed flag.
func (s *Server) restoreDefaultPlugin(name string, replace bool) error {
	bundled, ok := hostbridge.DefaultPluginByName(name)
	if !ok {
		return errors.New("没有找到这个默认插件")
	}
	if !bundled.Manifest.Supported() {
		return errors.New("这个插件不支持此系统")
	}
	if bundled.Manifest.Unavailable(version.Version, true) != "" {
		return errors.New("需要更新 Cage 才能安装这个插件")
	}

	dir := s.hostPluginDir()
	target := filepath.Join(dir, name)
	aside := ""
	if _, err := os.Lstat(target); err == nil {
		if !replace {
			return errors.New("这个插件已经安装")
		}
		s.closeHostTools()
		if aside, err = s.setAsideHostPlugin(name); err != nil {
			return err
		}
	}

	digest, err := hostbridge.InstallDefaultPlugin(name, dir)
	if err != nil {
		if aside != "" {
			_ = os.Rename(aside, target)
		}
		return err
	}

	s.defaultPluginsMu.Lock()
	records := s.readDefaultPluginsLocked()
	records[name] = defaultPluginRecord{
		InstalledVersion: bundled.Manifest.Version,
		DeployedSHA256:   digest,
		Dismissed:        false,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	writeErr := s.writeDefaultPluginsLocked(records)
	s.defaultPluginsMu.Unlock()
	if writeErr != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("write default-plugins.json error=%v", writeErr))
	}
	_ = s.recordHostPluginOn(name, true)
	return nil
}

func (s *Server) defaultPluginUpdateVersion(name, installedVersion string) string {
	bundled, ok := hostbridge.DefaultPluginByName(name)
	if !ok {
		return ""
	}
	if installedVersion == "" {
		return bundled.Manifest.Version
	}
	if cmp, valid := hostbridge.CompareVersions(bundled.Manifest.Version, installedVersion); valid && cmp > 0 {
		return bundled.Manifest.Version
	}
	return ""
}

func (s *Server) dismissedDefaultPluginViews(installedNames []string) []hostDefaultPluginView {
	s.defaultPluginsMu.Lock()
	records := s.readDefaultPluginsLocked()
	s.defaultPluginsMu.Unlock()

	var out []hostDefaultPluginView
	for _, bundled := range hostbridge.DefaultPlugins() {
		name := bundled.Manifest.Name
		if slices.Contains(installedNames, name) {
			continue
		}
		if !records[name].Dismissed {
			continue
		}
		unavailable := bundled.Manifest.Unavailable(version.Version, true)
		out = append(out, hostDefaultPluginView{
			Name:        name,
			DisplayName: bundled.Manifest.DisplayName,
			Version:     bundled.Manifest.Version,
			Description: bundled.Manifest.Description,
			I18n:        bundled.I18n,
			Kind:        bundled.Manifest.Kind,
			Permissions: bundled.Manifest.Permissions,
			Categories:  hostbridge.ShownCategories(bundled.Manifest.Categories),
			Contributes: hostContributes(bundled.Manifest.Contributes),
			Supported:   bundled.Manifest.Supported() && unavailable == "",
		})
	}
	return out
}
