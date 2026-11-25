package hostbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// DraftManifestFile is the neutral manifest filename authored inside extension
// drafts before the host workbench translates it into plugin.json.
const DraftManifestFile = "extension.json"

// DefaultDraftEngineRange is injected into translated plugin.json files when
// installing a neutral extension draft.
const DefaultDraftEngineRange = ">=0.2.0"

// DraftTarget values in neutral extension.json manifests.
const (
	DraftTargetWorkspace = "workspace"
	DraftTargetLocal     = "local"
)

// DraftManifest is the neutral manifest schema exposed to assistant-authored
// extensions. It carries no product or environment names; the host translates
// it into a standard Manifest during user-approved installation.
type DraftManifest struct {
	Name        string       `json:"name"`
	DisplayName string       `json:"displayName,omitempty"`
	Version     string       `json:"version"`
	Description string       `json:"description"`
	Icon        string       `json:"icon,omitempty"`
	Target      string       `json:"target"`
	Platforms   []string     `json:"platforms,omitempty"`
	Permissions []string     `json:"permissions"`
	Categories  []string     `json:"categories,omitempty"`
	Contributes *Contributes `json:"contributes"`
}

// ParseDraftManifest strictly parses and validates a neutral extension.json.
func ParseDraftManifest(data []byte) (DraftManifest, error) {
	var draft DraftManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return DraftManifest{}, fmt.Errorf("invalid %s: %w", DraftManifestFile, err)
	}
	if decoder.More() {
		return DraftManifest{}, fmt.Errorf("invalid %s: trailing content", DraftManifestFile)
	}
	if !ValidPluginName(draft.Name) {
		return DraftManifest{}, fmt.Errorf("invalid extension name %q", draft.Name)
	}
	if draft.Target != DraftTargetWorkspace && draft.Target != DraftTargetLocal {
		return DraftManifest{}, fmt.Errorf("target must be %q or %q", DraftTargetWorkspace, DraftTargetLocal)
	}
	if draft.Contributes == nil {
		return DraftManifest{}, errors.New("contributes is required")
	}
	if len(draft.Contributes.URIHandlers) > 0 {
		return DraftManifest{}, errors.New("uriHandlers are not supported in drafts")
	}
	if len(draft.Contributes.MCPServers) == 0 && len(draft.Contributes.Views) == 0 {
		return DraftManifest{}, errors.New("contributes must declare at least one mcpServer or view")
	}
	if len(draft.Contributes.MCPServers) > 1 {
		return DraftManifest{}, errors.New("at most one MCP server is supported")
	}
	if draft.Target == DraftTargetWorkspace && len(draft.Platforms) > 0 {
		return DraftManifest{}, errors.New("platforms is only allowed when target is local")
	}
	return draft, nil
}

// TranslateDraftManifest converts a neutral DraftManifest into a standard
// Manifest and pretty-printed plugin.json bytes, validating the result against
// parseManifest so every existing manifest invariant is enforced.
//
// If overrideKind is non-empty ("guest" or "host"), it overrides draft.Target.
func TranslateDraftManifest(draft DraftManifest, overrideKind, minEngine string) (Manifest, []byte, error) {
	kind := KindGuest
	if draft.Target == DraftTargetLocal {
		kind = KindHost
	}
	if overrideKind != "" {
		if overrideKind != KindGuest && overrideKind != KindHost {
			return Manifest{}, nil, fmt.Errorf("unsupported plugin kind %q", overrideKind)
		}
		kind = overrideKind
	}
	if minEngine == "" {
		minEngine = DefaultDraftEngineRange
		// A draft never names an engine range; one that uses view presentation
		// gets the first version that reads it.
		for _, view := range draft.Contributes.Views {
			if view.Presentation != "" || view.Width != 0 || view.Height != 0 {
				minEngine = fmt.Sprintf(">=%d.%d.%d", floatingViewsSince[0], floatingViewsSince[1], floatingViewsSince[2])
				break
			}
		}
	}
	platforms := draft.Platforms
	if kind == KindGuest {
		platforms = nil
	}
	perms := draft.Permissions
	if perms == nil {
		perms = []string{}
	}
	out := Manifest{
		Name:        draft.Name,
		DisplayName: draft.DisplayName,
		Version:     draft.Version,
		Description: draft.Description,
		Icon:        draft.Icon,
		Engines:     &Engines{Cage: minEngine},
		Kind:        kind,
		Platforms:   platforms,
		Permissions: perms,
		Categories:  draft.Categories,
		Contributes: draft.Contributes,
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return Manifest{}, nil, err
	}
	raw = append(raw, '\n')
	checked, err := parseManifest(raw)
	if err != nil {
		return Manifest{}, nil, err
	}
	return checked, raw, nil
}

var allowedViewExtensions = []string{
	".html", ".htm", ".js", ".mjs", ".css", ".json", ".txt",
	".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico",
	".woff", ".woff2", ".ttf", ".otf", ".wasm",
}

const maxDraftViewFileBytes = 20 << 20

// ValidateDraftDir performs host-side security and schema verification on an
// unpacked draft folder before installation. It rejects symlinks, oversized
// payloads, disallowed view extensions, invalid manifests, and name mismatches.
func ValidateDraftDir(dir, expectedName, overrideKind, minEngine string) (Manifest, []byte, error) {
	if !ValidPluginName(expectedName) {
		return Manifest{}, nil, errors.New("invalid plugin name")
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode().Type() != os.ModeDir {
		return Manifest{}, nil, errors.New("draft directory is not a plain folder")
	}
	var total int64
	files := 0
	err = filepath.WalkDir(dir, func(current string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, current)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if strings.Contains(relSlash, "\\") || slices.Contains(strings.Split(relSlash, "/"), "..") {
			return fmt.Errorf("unexpected path %q", relSlash)
		}
		linfo, err := os.Lstat(current)
		if err != nil {
			return err
		}
		switch linfo.Mode().Type() {
		case os.ModeDir:
			return nil
		case 0:
			files++
			total += linfo.Size()
			if files > marketFileLimit || linfo.Size() < 0 || total > marketUnpackedLimit {
				return errors.New("draft exceeds file count or size limit")
			}
			return nil
		default:
			return fmt.Errorf("%s is not a plain file or folder", relSlash)
		}
	})
	if err != nil {
		return Manifest{}, nil, err
	}

	rawDraft, err := os.ReadFile(filepath.Join(dir, DraftManifestFile))
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("missing %s: %w", DraftManifestFile, err)
	}
	draft, err := ParseDraftManifest(rawDraft)
	if err != nil {
		return Manifest{}, nil, err
	}
	if draft.Name != expectedName {
		return Manifest{}, nil, fmt.Errorf("draft name %q does not match %q", draft.Name, expectedName)
	}
	manifest, manifestBytes, err := TranslateDraftManifest(draft, overrideKind, minEngine)
	if err != nil {
		return Manifest{}, nil, err
	}

	// Verify view files on the host side too.
	for _, view := range manifest.Contributes.Views {
		entryPath := filepath.Join(dir, filepath.FromSlash(view.Entry))
		entryInfo, err := os.Lstat(entryPath)
		if err != nil || !entryInfo.Mode().IsRegular() {
			return Manifest{}, nil, fmt.Errorf("view %s entry %q is missing", view.ID, view.Entry)
		}
		viewDir := filepath.Dir(entryPath)
		err = filepath.WalkDir(viewDir, func(current string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			linfo, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if linfo.IsDir() {
				return nil
			}
			if !linfo.Mode().IsRegular() {
				return fmt.Errorf("view %s contains non-regular file", view.ID)
			}
			if linfo.Size() > maxDraftViewFileBytes {
				return fmt.Errorf("view %s file exceeds 20 MiB limit", view.ID)
			}
			ext := strings.ToLower(path.Ext(filepath.ToSlash(current)))
			if !slices.Contains(allowedViewExtensions, ext) {
				return fmt.Errorf("view %s contains disallowed file type %q", view.ID, ext)
			}
			return nil
		})
		if err != nil {
			return Manifest{}, nil, err
		}
	}

	return manifest, manifestBytes, nil
}
