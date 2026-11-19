package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

// Tailscale runs as a plugin inside the environment: its node is the
// plugin's MCP server, started and stopped with the other plugins. The
// program itself is installed in the environment as before.
const tailnetPlugin = "tailscale"

const tailnetPluginManifest = `{
  "name": "tailscale",
  "displayName": "Tailscale",
  "version": "1.104.5",
  "description": "A private network between your devices: reach their services and sign in to them over SSH.",
  "engines": { "cage": ">=0.1.8" },
  "kind": "guest",
  "permissions": ["network"],
  "categories": ["network"],
  "contributes": {
    "mcpServers": [
      { "id": "node", "command": ["/usr/local/bin/tailnet", "mcp"] }
    ]
  }
}
`

// tailnetTools are the plugin's tools for the actions of the workbench.
var tailnetTools = map[string]string{
	"status": "status", "connect": "connect", "login": "login", "disconnect": "disconnect", "logout": "logout",
	"add-key": "add_key", "remove-key": "remove_key", "open-service": "open_service",
}

func (s *Server) tailnetPluginDir() string { return filepath.Join(s.hostPluginDir(), tailnetPlugin) }

// tailnetPluginInstalled reports whether the plugin's folder is there.
func (s *Server) tailnetPluginInstalled() bool {
	_, err := os.Stat(filepath.Join(s.tailnetPluginDir(), hostbridge.ManifestFile))
	return err == nil
}

// ensureTailnetPlugin writes the plugin's folder once the program is
// installed in the environment, and reports whether anything changed. The
// user asked for Tailscale by installing it, so its actions are not asked
// about again; the grant can be taken back on the plugin's page like any
// other.
func (s *Server) ensureTailnetPlugin() (bool, error) {
	path := filepath.Join(s.tailnetPluginDir(), hostbridge.ManifestFile)
	if current, err := os.ReadFile(path); err == nil && string(current) == tailnetPluginManifest {
		return false, nil
	}
	fresh := !s.tailnetPluginInstalled()
	if err := os.MkdirAll(s.tailnetPluginDir(), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(tailnetPluginManifest), 0o644); err != nil {
		return false, err
	}
	if fresh {
		if err := s.hostGrantStore().grant(hostGrantPlugin+tailnetPlugin, []string{"network"}); err != nil {
			return true, err
		}
	}
	return true, nil
}

var errTailnetNotRunning = errors.New("the plugin is not running")

// callTailnet calls one of the running plugin's tools, as its page would.
func (s *Server) callTailnet(ctx context.Context, tool string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	if s.tailnetTool != nil {
		return s.tailnetTool(ctx, tool, arguments)
	}
	s.mu.Lock()
	tools := s.hostTools
	s.mu.Unlock()
	if tools != nil {
		for _, plugin := range tools.plugins {
			if plugin.Manifest.Name == tailnetPlugin {
				return plugin.CallFromView(ctx, tool, arguments, 0)
			}
		}
	}
	return nil, errTailnetNotRunning
}
