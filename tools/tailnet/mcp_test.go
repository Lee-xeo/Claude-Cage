package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The node speaks MCP on request: its actions are tools, every one of them
// for its own page alone, and a call gives back the state the page shows.
func TestNodeServesItsActionsAsToolsForItsPage(t *testing.T) {
	n, err := newNode(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"add_key","arguments":{"key":"not a key"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"add_key","arguments":{"key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl laptop"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"browser_return"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"shell"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"resources/list"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	serveMCP(n, strings.NewReader(requests), &out)
	replies := map[float64]map[string]any{}
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		var reply map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			t.Fatalf("%v: %s", err, scanner.Text())
		}
		replies[reply["id"].(float64)] = reply
	}
	if len(replies) != 8 {
		t.Fatalf("%d replies; a notification must get none", len(replies))
	}
	result := func(id float64) map[string]any {
		value, _ := replies[id]["result"].(map[string]any)
		if value == nil {
			t.Fatalf("request %v: %v", id, replies[id])
		}
		return value
	}
	if result(1)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", result(1))
	}
	tools := result(2)["tools"].([]any)
	var names []string
	for _, entry := range tools {
		tool := entry.(map[string]any)
		names = append(names, tool["name"].(string))
		audience := tool["_meta"].(map[string]any)["ui"].(map[string]any)["visibility"].([]any)
		if len(audience) != 1 || audience[0] != "app" || tool["inputSchema"] == nil || tool["description"] == "" {
			t.Fatalf("tool %v must be for the page alone and described", tool)
		}
		// What a tool says of itself may be shown to a model one day.
		for _, word := range []string{"cage", "guest", "virtual", " vm"} {
			if strings.Contains(strings.ToLower(tool["name"].(string)+" "+tool["description"].(string)), word) {
				t.Fatalf("tool %v says where it runs", tool["name"])
			}
		}
	}
	if strings.Join(names, " ") != "status connect login disconnect logout add_key remove_key open_service browser_return" {
		t.Fatalf("tools %v", names)
	}
	status := result(3)
	shown, _ := status["structuredContent"].(map[string]any)
	if status["isError"] != nil || shown == nil || shown["enabled"] != false || shown["version"] != version {
		t.Fatalf("status: %v", status)
	}
	if result(4)["isError"] != true {
		t.Fatalf("a bad key was accepted: %v", result(4))
	}
	added := result(5)["structuredContent"].(map[string]any)["keys"].([]any)
	if len(added) != 1 || !strings.HasPrefix(added[0].(string), "ssh-ed25519 ") {
		t.Fatalf("keys %v", added)
	}
	if result(6)["isError"] != nil {
		t.Fatalf("browser return: %v", result(6))
	}
	if _, err := os.Stat(filepath.Join(n.dir, "browser-return")); err != nil {
		t.Fatal("the return from the browser was not noted")
	}
	for _, id := range []float64{7, 8} {
		if replies[id]["error"] == nil {
			t.Fatalf("request %v was answered: %v", id, replies[id])
		}
	}
}
