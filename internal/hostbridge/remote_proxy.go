package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxProxyTools = 256

// ProxyCall invokes a proxied model tool over an authenticated reverse channel.
type ProxyCall func(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error)

// ProxySkipped records why a proxied tool was not exposed alongside base tools.
type ProxySkipped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// ProxyPromptGroup describes one proxied plugin whose accepted tools can be
// invoked through a slash prompt.
type ProxyPromptGroup struct {
	Name        string
	DisplayName string
	Description string
	Tools       []string
}

type proxyPromptEntry struct {
	prompt  *mcp.Prompt
	handler mcp.PromptHandler
}

// ProxyPlugin exposes a validated snapshot of model-visible tools provided by
// a connected desktop client over a reverse tunnel. It never shadows tools
// or prompts already registered by the server's own plugins.
type ProxyPlugin struct {
	tools   []*mcp.Tool
	prompts []proxyPromptEntry
	call    ProxyCall
	skipped []ProxySkipped
}

// NewProxyPlugin validates tools against reserved wording and taken names.
// Valid, non-conflicting tools are kept; rejected tools are reported in skipped.
func NewProxyPlugin(tools []*mcp.Tool, taken map[string]bool, call ProxyCall) (*ProxyPlugin, error) {
	if call == nil {
		return nil, errors.New("proxy call handler is required")
	}
	if len(tools) > maxProxyTools {
		return nil, fmt.Errorf("too many proxied tools (%d > %d)", len(tools), maxProxyTools)
	}
	seen := map[string]bool{}
	var accepted []*mcp.Tool
	var skipped []ProxySkipped
	for _, raw := range tools {
		if raw == nil {
			continue
		}
		name := strings.TrimSpace(raw.Name)
		if name == "" || len(name) > 200 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			skipped = append(skipped, ProxySkipped{Name: raw.Name, Reason: "invalid tool name"})
			continue
		}
		cloned := *raw
		cloned.Name = name
		if cloned.InputSchema == nil {
			cloned.InputSchema = map[string]any{"type": "object"}
		}
		if !toolFor(&cloned, forModel) {
			skipped = append(skipped, ProxySkipped{Name: name, Reason: "not offered to the model"})
			continue
		}
		if word, found := CheckWording(&cloned); found {
			skipped = append(skipped, ProxySkipped{Name: name, Reason: fmt.Sprintf("mentions reserved word %q", word)})
			continue
		}
		if err := validateToolSchema(&cloned); err != nil {
			skipped = append(skipped, ProxySkipped{Name: name, Reason: "invalid tool schema"})
			continue
		}
		if taken[name] {
			skipped = append(skipped, ProxySkipped{Name: name, Reason: "name already taken by remote plugin"})
			continue
		}
		if seen[name] {
			skipped = append(skipped, ProxySkipped{Name: name, Reason: "duplicate proxied tool name"})
			continue
		}
		seen[name] = true
		accepted = append(accepted, &cloned)
	}
	return &ProxyPlugin{tools: accepted, call: call, skipped: skipped}, nil
}

// validateToolSchema verifies that mcp.Server.AddTool accepts the tool without
// panicking so a malformed proxied definition can never crash the bridge.
func validateToolSchema(tool *mcp.Tool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid tool schema: %v", r)
		}
	}()
	probe := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "1"}, nil)
	probe.AddTool(tool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	return nil
}

// Tools reports the model tool names registered by this proxy.
func (p *ProxyPlugin) Tools() []string {
	names := make([]string, len(p.tools))
	for i, tool := range p.tools {
		names[i] = tool.Name
	}
	return names
}

// ToolList returns summary metadata for the accepted tools.
func (p *ProxyPlugin) ToolList() []ToolInfo {
	list := make([]ToolInfo, len(p.tools))
	for i, tool := range p.tools {
		list[i] = ToolInfo{Name: tool.Name, Description: tool.Description}
	}
	return list
}

// SetPluginPrompts builds slash prompts for proxied plugins whose accepted
// tools are present in this proxy, skipping any prompt name already taken.
func (p *ProxyPlugin) SetPluginPrompts(groups []ProxyPromptGroup, taken map[string]bool) {
	accepted := map[string]bool{}
	for _, tool := range p.tools {
		accepted[tool.Name] = true
	}
	seen := map[string]bool{}
	var entries []proxyPromptEntry
	for _, group := range groups {
		name := strings.TrimSpace(group.Name)
		if name == "" || taken[name] || seen[name] {
			continue
		}
		var tools []string
		for _, toolName := range group.Tools {
			if accepted[toolName] {
				tools = append(tools, toolName)
			}
		}
		prompt, handler := buildPluginPrompt(name, group.DisplayName, group.Description, tools)
		if prompt == nil || handler == nil {
			continue
		}
		seen[prompt.Name] = true
		entries = append(entries, proxyPromptEntry{prompt: prompt, handler: handler})
	}
	p.prompts = entries
}

// Prompts reports the prompt names registered by this proxy.
func (p *ProxyPlugin) Prompts() []string {
	names := make([]string, len(p.prompts))
	for i, entry := range p.prompts {
		names[i] = entry.prompt.Name
	}
	return names
}

// PromptList returns summary metadata for the proxy's prompts.
func (p *ProxyPlugin) PromptList() []PromptInfo {
	list := make([]PromptInfo, len(p.prompts))
	for i, entry := range p.prompts {
		list[i] = PromptInfo{Name: entry.prompt.Name, Description: entry.prompt.Description}
	}
	return list
}

// Skipped returns tools that were filtered out during registration.
func (p *ProxyPlugin) Skipped() []ProxySkipped {
	return append([]ProxySkipped(nil), p.skipped...)
}

// Register adds all accepted proxied tools and prompts to server.
func (p *ProxyPlugin) Register(server *mcp.Server) {
	for _, tool := range p.tools {
		toolName := tool.Name
		server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args json.RawMessage
			if request != nil && request.Params != nil {
				args = request.Params.Arguments
			}
			result, err := p.call(ctx, toolName, args)
			if err != nil {
				failed := &mcp.CallToolResult{}
				failed.SetError(err)
				return failed, nil
			}
			if result == nil {
				return &mcp.CallToolResult{}, nil
			}
			return result, nil
		})
	}
	for _, entry := range p.prompts {
		server.AddPrompt(entry.prompt, entry.handler)
	}
}
