package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"
)

// The node's actions as MCP tools, for a page that shows the node to the
// user. All of them are for that page alone: joining and leaving a network
// and deciding who may sign in are the user's to do by hand.
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Meta        map[string]any `json:"_meta"`
	action      string
}

func mcpTools() []mcpTool {
	none := map[string]any{"type": "object", "properties": map[string]any{}}
	key := map[string]any{"type": "object", "required": []string{"key"},
		"properties": map[string]any{"key": map[string]any{"type": "string", "description": "one SSH public key line"}}}
	service := map[string]any{"type": "object", "required": []string{"host", "port"}, "properties": map[string]any{
		"host": map[string]any{"type": "string", "description": "name or address of a device on the network"},
		"port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}}}
	page := map[string]any{"ui": map[string]any{"visibility": []string{"app"}}}
	tools := []mcpTool{
		{Name: "status", Description: "Report the Tailscale connection, its devices and the SSH keys allowed to sign in.", InputSchema: none, action: "status"},
		{Name: "connect", Description: "Join the Tailscale network, starting sign-in if this device is not signed in.", InputSchema: none, action: "connect"},
		{Name: "login", Description: "Start signing in to Tailscale again.", InputSchema: none, action: "login"},
		{Name: "disconnect", Description: "Leave the Tailscale network, staying signed in.", InputSchema: none, action: "disconnect"},
		{Name: "logout", Description: "Sign this device out of Tailscale.", InputSchema: none, action: "logout"},
		{Name: "add_key", Description: "Allow an SSH public key to sign in over the network.", InputSchema: key, action: "add-key"},
		{Name: "remove_key", Description: "Stop allowing an SSH public key.", InputSchema: key, action: "remove-key"},
		{Name: "open_service", Description: "Make a port of a device on the network reachable on this system's loopback address.", InputSchema: service, action: "open-service"},
		{Name: "browser_return", Description: "Note that the browser came back from signing in.", InputSchema: none, action: "browser-return"},
	}
	for i := range tools {
		tools[i].Meta = page
	}
	return tools
}

type mcpMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string  `json:"protocolVersion"`
		Name            string  `json:"name"`
		Arguments       request `json:"arguments"`
	} `json:"params"`
}

// serveMCP answers MCP requests, one JSON object a line, until in ends.
func serveMCP(n *node, in io.Reader, out io.Writer) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var message mcpMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil || len(message.ID) == 0 {
			continue // not a request: a notification needs no reply
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": message.ID}
		if result := n.answerMCP(message); result != nil {
			reply["result"] = result
		} else {
			reply["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		_ = encoder.Encode(reply)
	}
}

func (n *node) answerMCP(message mcpMessage) any {
	switch message.Method {
	case "initialize":
		return map[string]any{"protocolVersion": message.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "tailnet", "version": version}}
	case "ping":
		return map[string]any{}
	case "tools/list":
		return map[string]any{"tools": mcpTools()}
	case "tools/call":
		for _, tool := range mcpTools() {
			if tool.Name == message.Params.Name {
				return n.callMCP(tool.action, message.Params.Arguments)
			}
		}
	}
	return nil
}

// callMCP runs one action and gives the node's state, as the page shows it,
// or the reason the action failed.
func (n *node) callMCP(action string, arguments request) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var result state
	var err error
	if action == "browser-return" {
		if err = recordBrowserReturn(n.dir); err == nil {
			result, err = n.run(ctx, request{Action: "status"})
		}
	} else {
		arguments.Action = action
		result, err = n.run(ctx, arguments)
	}
	if err != nil {
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
	}
	text, _ := json.Marshal(result)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}, "structuredContent": result}
}
