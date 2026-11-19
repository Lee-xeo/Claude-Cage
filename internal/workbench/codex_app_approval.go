package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

// Accept only the bundled Computer Use app-access form. Other requests can
// carry URL challenges, arbitrary fields or different authorization scopes.
type codexAppRequest struct {
	Server string `json:"serverName"`
	Mode   string `json:"mode"`
	Schema struct {
		Type       string
		Properties map[string]json.RawMessage
		Required   []string
	} `json:"requestedSchema"`
	Meta struct {
		Kind      string                     `json:"codex_approval_kind"`
		Connector string                     `json:"connector_id"`
		Tool      string                     `json:"tool_name"`
		Args      map[string]json.RawMessage `json:"tool_params"`
		Persist   []string                   `json:"persist"`
	} `json:"_meta"`
}

var appBundleID = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

func (r codexAppRequest) bundleID() string {
	var id string
	if r.Server != "cua_repl" || r.Mode != "form" || r.Meta.Kind != "mcp_tool_call" || r.Meta.Connector != "computer-use" || r.Meta.Tool != "get_app_state" || r.Schema.Type != "object" || r.Schema.Properties == nil || len(r.Schema.Properties) != 0 || len(r.Schema.Required) != 0 || len(r.Meta.Args) != 1 || json.Unmarshal(r.Meta.Args["app"], &id) != nil || len(id) > 255 || !appBundleID.MatchString(id) {
		return ""
	}
	return id
}

func (c *codexConversation) requestReply(m codexEvent) map[string]any {
	reply := map[string]any{"id": *m.ID}
	if m.Method == "item/commandExecution/requestApproval" {
		r := m.Params.codexNetworkRequest
		decision := "cancel"
		if c.thread != "" && c.turn != "" && m.Params.ThreadID == c.thread && m.Params.TurnID == c.turn {
			c.commandNotice = "Codex did not run this command. The requested approval scope is unavailable."
			if c.approveNetwork != nil && r.networkOnly() {
				c.commandNotice = "Codex did not run this command. The host did not grant network access for this invocation."
				if c.approveNetwork(r) {
					decision = "accept"
					c.commandNotice = ""
				}
			}
		}
		reply["result"] = map[string]string{"decision": decision}
		return reply
	}
	if m.Method != "mcpServer/elicitation/request" {
		reply["error"] = map[string]any{"code": -32601, "message": "Interaction unavailable"}
		return reply
	}
	result := map[string]any{"action": "decline", "content": nil, "_meta": nil}
	r := m.Params.codexAppRequest
	if c.approve != nil && c.thread != "" && c.turn != "" && m.Params.ThreadID == c.thread && m.Params.TurnID == c.turn && r.bundleID() != "" && c.approve(r) {
		result["action"] = "accept"
		result["content"] = map[string]any{}
		if slices.Contains(r.Meta.Persist, "always") {
			result["_meta"] = map[string]string{"persist": "always"}
		}
	}
	reply["result"] = result
	return reply
}

// Only the installed native application can return the user's decision. Killing
// the helper on lock, timeout or session close dismisses its system dialog.
func confirmCodexApp(ctx context.Context, project string, r codexAppRequest) bool {
	scope := "once"
	if slices.Contains(r.Meta.Persist, "always") {
		scope = "always"
	}
	return runCodexConfirmation(ctx, nil, "--confirm-codex-app", project, r.bundleID(), scope)
}

func runCodexConfirmation(ctx context.Context, input []byte, args ...string) bool {
	exe, err := os.Executable()
	if err != nil || filepath.Base(filepath.Dir(exe)) != "Resources" {
		return false
	}
	contents := filepath.Dir(filepath.Dir(exe))
	if filepath.Base(contents) != "Contents" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(contents, "MacOS", "Cage"), args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Run() == nil && ctx.Err() == nil
}
