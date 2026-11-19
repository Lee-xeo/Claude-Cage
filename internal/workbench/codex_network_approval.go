package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// The approval adds network access to one sandboxed exec_command. Never map
// a request for unrestricted execution or a persistent rule to this decision.
type codexNetworkRequest struct {
	Kind          string
	ItemID        string `json:"itemId"`
	EnvironmentID string `json:"environmentId"`
	Command       string
	Cwd           string
	Additional    json.RawMessage   `json:"additionalPermissions"`
	Decisions     []json.RawMessage `json:"availableDecisions"`
}

func (r codexNetworkRequest) networkOnly() bool {
	var p struct {
		Network    *struct{ Enabled bool }
		FileSystem json.RawMessage
	}
	d := json.NewDecoder(bytes.NewReader(r.Additional))
	d.DisallowUnknownFields()
	if r.Kind != "command" || r.ItemID == "" || (r.EnvironmentID != "" && r.EnvironmentID != "local") || d.Decode(&p) != nil || p.Network == nil || !p.Network.Enabled || (len(p.FileSystem) > 0 && string(p.FileSystem) != "null") {
		return false
	}
	if !filepath.IsAbs(r.Cwd) || strings.TrimSpace(r.Command) == "" || len(r.Command) > 16384 || strings.ContainsFunc(r.Command+r.Cwd, func(v rune) bool {
		return (unicode.IsControl(v) && v != '\n' && v != '\t') || unicode.Is(unicode.Cf, v)
	}) {
		return false
	}
	return slices.ContainsFunc(r.Decisions, func(v json.RawMessage) bool { return string(v) == `"accept"` })
}

func confirmCodexNetwork(ctx context.Context, project, session string, r codexNetworkRequest) bool {
	cwd, err := filepath.EvalSymlinks(r.Cwd)
	if err != nil {
		return false
	}
	for _, root := range []string{project, session} {
		real, err := filepath.EvalSymlinks(root)
		if err != nil || (cwd != real && !strings.HasPrefix(cwd, real+string(filepath.Separator))) {
			continue
		}
		data, err := json.Marshal(map[string]string{"cwd": cwd, "project": project, "command": r.Command})
		return err == nil && runCodexConfirmation(ctx, data, "--confirm-codex-network")
	}
	return false
}
