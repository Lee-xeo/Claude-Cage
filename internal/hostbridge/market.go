package hostbridge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Market is a published list of plugins and where their archives live.
type Market struct {
	// IndexURL is the list. ArchivePrefix is what every archive address in it
	// must start with, so the list cannot point anywhere else.
	IndexURL      string
	ArchivePrefix string
	// IconPrefix is what a plugin's picture address must start with.
	IconPrefix string
}

// CommunityMarket is the public plugin repository.
var CommunityMarket = Market{
	IndexURL:      "https://raw.githubusercontent.com/Lee-xeo/cage-plugins/main/index.json",
	ArchivePrefix: "https://raw.githubusercontent.com/Lee-xeo/cage-plugins/main/dist/",
	IconPrefix:    "https://raw.githubusercontent.com/Lee-xeo/cage-plugins/main/plugins/",
}

// MarketEntry is one plugin in the list.
type MarketEntry struct {
	Name        string                   `json:"name"`
	DisplayName string                   `json:"displayName,omitempty"`
	Version     string                   `json:"version"`
	Description string                   `json:"description"`
	Author      *MarketAuthor            `json:"author,omitempty"`
	I18n        map[string]LocalizedText `json:"i18n,omitempty"`
	Platforms   []string                 `json:"platforms,omitempty"`
	Permissions []string      `json:"permissions"`
	Categories  []string      `json:"categories,omitempty"`
	// Kind, Engines and Contributes repeat the plugin's manifest. Kind is
	// host when the list leaves it out.
	Kind        string       `json:"kind,omitempty"`
	Engines     *Engines     `json:"engines,omitempty"`
	Contributes *Contributes `json:"contributes,omitempty"`
	// Target is the system and processor this archive is for, as Target
	// gives it. Empty means any.
	Target  string `json:"target,omitempty"`
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	// Icon is the address of the plugin's picture, if it has one.
	Icon string `json:"icon,omitempty"`
	// Runtime names the runtime program the plugin uses (such as python3, node,
	// npx, uv or powershell), when the list includes it. Only shown, never acted on.
	Runtime string `json:"runtime,omitempty"`
	// Source names the open-source project the plugin is built on, when it
	// is. It is only shown, never acted on.
	Source *Source `json:"source,omitempty"`
	// AllPlatforms collects the union of declared platforms across all target
	// entries of the same plugin in the market list, for display. Empty means
	// all platforms (darwin, linux, windows).
	AllPlatforms []string `json:"-"`
}

// Source is the project a plugin packages or builds on.
type Source struct {
	Project string `json:"project"`
	Version string `json:"version,omitempty"`
	// License is an SPDX expression, as MIT or Apache-2.0.
	License string `json:"license,omitempty"`
	// URL is the project's https home page.
	URL string `json:"url,omitempty"`
}

var sourceLicense = regexp.MustCompile(`^[A-Za-z0-9.+-]+( (AND|OR|WITH) [A-Za-z0-9.+-]+)*$`)

// valid reports whether every field is short plain text, and the address
// https to a named host.
func (s *Source) valid() bool {
	plain := func(text string, limit int) bool {
		return len(text) <= limit && !strings.ContainsFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f })
	}
	if strings.TrimSpace(s.Project) == "" || !plain(s.Project, 100) || !plain(s.Version, 40) || len(s.License) > 60 ||
		s.License != "" && !sourceLicense.MatchString(s.License) {
		return false
	}
	if s.URL == "" {
		return true
	}
	address, err := url.Parse(s.URL)
	return err == nil && len(s.URL) <= 300 && address.Scheme == "https" && hostName.MatchString(address.Hostname()) &&
		address.User == nil && address.Port() == ""
}

// MarketAuthor is the attribution published by the market for a listing.
type MarketAuthor struct {
	Name     string `json:"name"`
	Official bool   `json:"official,omitempty"`
}

// LocalizedText is a plugin's display name and description in one language.
type LocalizedText struct {
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

// I18nFile is the optional translation table kept beside plugin.json.
const (
	I18nFile  = "i18n.json"
	I18nLimit = 256 << 10
)

var localeTag = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)

func plainText(text string, limit int) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > limit || strings.ContainsFunc(trimmed, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false
	}
	return trimmed, true
}

func sanitizeI18n(raw map[string]LocalizedText) map[string]LocalizedText {
	if len(raw) == 0 {
		return nil
	}
	clean := make(map[string]LocalizedText, len(raw))
	for code, entry := range raw {
		if !localeTag.MatchString(code) || len(code) > 20 {
			continue
		}
		var item LocalizedText
		if name, ok := plainText(entry.DisplayName, 80); ok {
			item.DisplayName = name
		}
		if desc, ok := plainText(entry.Description, 600); ok {
			item.Description = desc
		}
		if item.DisplayName == "" && item.Description == "" {
			continue
		}
		clean[code] = item
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

// LoadI18n reads the optional i18n.json inside an installed plugin folder.
// Missing, linked or malformed files yield nil so plugins still load cleanly.
func LoadI18n(dir string) map[string]LocalizedText {
	file := filepath.Join(dir, I18nFile)
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > I18nLimit {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var raw map[string]LocalizedText
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return sanitizeI18n(raw)
}

// FetchIcon downloads the entry's picture and returns it with its media type.
func (m Market) FetchIcon(ctx context.Context, client *http.Client, entry MarketEntry) ([]byte, string, error) {
	if entry.Icon == "" {
		return nil, "", os.ErrNotExist
	}
	data, err := fetchLimited(ctx, client, entry.Icon, IconLimit)
	return data, IconType(entry.Icon), err
}

// Supported reports whether the plugin runs on this system.
func (e MarketEntry) Supported() bool {
	return Manifest{Platforms: e.Platforms}.Supported() && (e.Target == "" || e.Target == Target(e.Kind))
}

// EngineSatisfied reports whether the application at version can run it.
func (e MarketEntry) EngineSatisfied(version string) bool {
	return Manifest{Engines: e.Engines, Contributes: e.Contributes}.EngineSatisfied(version)
}

var (
	marketTarget  = regexp.MustCompile(`^(darwin|linux|win32)-(x64|arm64)$`)
	marketRuntime = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
)

func mergePlatforms(existing, added []string, allKnown bool) ([]string, bool) {
	order := []string{"darwin", "windows", "linux"}
	if allKnown || len(added) == 0 {
		return slices.Clone(order), true
	}
	merged := slices.Clone(existing)
	for _, p := range added {
		if !slices.Contains(merged, p) {
			merged = append(merged, p)
		}
	}
	var ordered []string
	for _, p := range order {
		if slices.Contains(merged, p) {
			ordered = append(ordered, p)
		}
	}
	return ordered, len(ordered) == len(order)
}

const (
	marketIndexLimit   = 4 << 20
	marketArchiveLimit = 20 << 20
	// What an archive may unpack to.
	marketUnpackedLimit = 100 << 20
	marketFileLimit     = 2000
)

// Fetch reads the list. Entries that are not well formed are left out.
func (m Market) Fetch(ctx context.Context, client *http.Client) ([]MarketEntry, error) {
	data, err := fetchLimited(ctx, client, m.IndexURL, marketIndexLimit)
	if err != nil {
		return nil, err
	}
	var index struct {
		Version int           `json:"version"`
		Plugins []MarketEntry `json:"plugins"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("plugin list: %w", err)
	}
	if index.Version != 1 {
		return nil, fmt.Errorf("plugin list has version %d; this application reads version 1", index.Version)
	}
	var entries []MarketEntry
	seen := map[string]int{}
	allPlats := map[string]bool{}
	for _, entry := range index.Plugins {
		if entry.Kind == "" {
			entry.Kind = KindHost
		}
		// A plugin may be listed once for each target; the one for this
		// system is kept, or the first if none is.
		if !m.valid(entry) {
			continue
		}
		if at, listed := seen[entry.Name]; listed {
			merged, anyAll := mergePlatforms(entries[at].AllPlatforms, entry.Platforms, allPlats[entry.Name])
			allPlats[entry.Name] = anyAll
			if !entries[at].Supported() && entry.Supported() {
				entries[at] = m.shown(entry)
			}
			entries[at].AllPlatforms = merged
			continue
		}
		shown := m.shown(entry)
		merged, anyAll := mergePlatforms(nil, entry.Platforms, entry.Kind == KindGuest)
		allPlats[entry.Name] = anyAll
		shown.AllPlatforms = merged
		seen[entry.Name] = len(entries)
		entries = append(entries, shown)
	}
	return entries, nil
}

// shown is the entry as kept from the list.
func (m Market) shown(entry MarketEntry) MarketEntry {
	if entry.Permissions == nil {
		entry.Permissions = []string{}
	}
	// A source that is not well formed is not shown; the plugin still is.
	if entry.Source != nil && !entry.Source.valid() {
		entry.Source = nil
	}
	if entry.Runtime != "" && !marketRuntime.MatchString(entry.Runtime) {
		entry.Runtime = ""
	}
	if entry.Author != nil {
		if name, ok := plainText(entry.Author.Name, 80); ok {
			entry.Author = &MarketAuthor{Name: name, Official: entry.Author.Official}
		} else {
			entry.Author = nil
		}
	}
	entry.I18n = sanitizeI18n(entry.I18n)
	// A picture from anywhere else is not shown; the plugin still is.
	if rest := strings.TrimPrefix(entry.Icon, m.IconPrefix+entry.Name+"/"); m.IconPrefix == "" || rest == entry.Icon || !validIcon(rest) || strings.ContainsAny(rest, "?#%") {
		entry.Icon = ""
	}
	return entry
}

func (m Market) valid(entry MarketEntry) bool {
	if !pluginName.MatchString(entry.Name) || len(entry.Name) > 40 || entry.Version == "" || len(entry.Version) > 40 {
		return false
	}
	if digest, err := hex.DecodeString(entry.SHA256); err != nil || len(digest) != sha256.Size {
		return false
	}
	if entry.Bytes <= 0 || entry.Bytes > marketArchiveLimit {
		return false
	}
	if !strings.HasPrefix(entry.Archive, m.ArchivePrefix) || strings.ContainsAny(strings.TrimPrefix(entry.Archive, m.ArchivePrefix), "/?#%\\") {
		return false
	}
	for _, permission := range entry.Permissions {
		if !slices.Contains(KnownPermissions, permission) {
			return false
		}
	}
	if entry.Kind != "" && entry.Kind != KindHost && entry.Kind != KindGuest || len(entry.DisplayName) > 80 {
		return false
	}
	if entry.Engines != nil && !engineRange.MatchString(entry.Engines.Cage) {
		return false
	}
	return entry.Target == "" || marketTarget.MatchString(entry.Target)
}

// Install downloads the entry's archive, checks it against the digest in the
// list and unpacks it into the plugin's own folder under dir. It never
// replaces a folder that is already there.
func (m Market) Install(ctx context.Context, client *http.Client, entry MarketEntry, dir string) error {
	if !m.valid(entry) {
		return errors.New("the plugin list entry is not valid")
	}
	target := filepath.Join(dir, entry.Name)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("plugin %s is already installed", entry.Name)
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := fetchLimited(ctx, client, entry.Archive, entry.Bytes)
	if err != nil {
		return err
	}
	if digest := sha256.Sum256(data); hex.EncodeToString(digest[:]) != strings.ToLower(entry.SHA256) {
		return errors.New("the downloaded plugin does not match the digest in the plugin list")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(dir, ".installing-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := unpackPlugin(data, entry.Name, staging); err != nil {
		return err
	}
	manifest, err := LoadManifest(filepath.Join(staging, entry.Name))
	if err != nil {
		return err
	}
	if manifest.Name != entry.Name || manifest.Version != entry.Version || entry.Kind != "" && manifest.Kind != entry.Kind {
		return errors.New("the plugin's own manifest does not match the plugin list")
	}
	if !slices.Equal(slices.Sorted(slices.Values(manifest.Permissions)), slices.Sorted(slices.Values(entry.Permissions))) {
		return errors.New("the plugin declares other permissions than the plugin list shows")
	}
	return os.Rename(filepath.Join(staging, entry.Name), target)
}

// unpackPlugin writes the archive's files under into. Everything must be a
// plain file or folder inside the folder called name.
func unpackPlugin(data []byte, name, into string) error {
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	archive := tar.NewReader(compressed)
	var total int64
	files := 0
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		clean := path.Clean(header.Name)
		if clean != header.Name && clean+"/" != header.Name || path.IsAbs(clean) || strings.Contains(clean, "\\") ||
			clean != name && !strings.HasPrefix(clean, name+"/") || slices.Contains(strings.Split(clean, "/"), "..") {
			return fmt.Errorf("the plugin archive holds an unexpected path %q", header.Name)
		}
		to := filepath.Join(into, filepath.FromSlash(clean))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(to, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			total += header.Size
			if files > marketFileLimit || header.Size < 0 || total > marketUnpackedLimit {
				return errors.New("the plugin archive is too large")
			}
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
			if err != nil {
				return err
			}
			_, err = io.CopyN(file, archive, header.Size)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the plugin archive holds %q, which is neither a file nor a folder", header.Name)
		}
	}
	return nil
}

func fetchLimited(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", request.URL.Host, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("the download is larger than expected")
	}
	return data, nil
}
