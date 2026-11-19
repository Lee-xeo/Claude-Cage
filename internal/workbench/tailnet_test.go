package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

func TestTailnetAuthorizationURL(t *testing.T) {
	for _, u := range []string{"https://login.tailscale.com/a/abc", "https://login.tailscale.com/c/abc"} {
		if !validTailnetAuthURL(u) {
			t.Fatal(u)
		}
	}
	for _, u := range []string{"http://login.tailscale.com/a/abc", "https://login.tailscale.com.evil.test/a/abc", "https://evil.test/a/abc", "https://user@login.tailscale.com/a/abc", "https://login.tailscale.com:444/a/abc", "javascript:alert(1)", "https://login.tailscale.com/admin"} {
		if validTailnetAuthURL(u) {
			t.Fatal(u)
		}
	}
}
func TestTailnetBoundaries(t *testing.T) {
	s := testServer(t)
	if w := request(s, "POST", "/api/tailscale/connect", "https://evil.test", "{}", true); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(s, "GET", "/api/tailscale", "", "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(s, "GET", "/api/tailscale", "", "", true); w.Code != 409 {
		t.Fatal(w.Code)
	}
}

// The workbench's actions go to the plugin's tools; what a tool reports,
// state or failure, reaches the panel as before.
func TestTailnetActionsGoToThePlugin(t *testing.T) {
	s := testServer(t)
	s.ready = true
	var called []string
	s.tailnetTool = func(_ context.Context, tool string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
		called = append(called, tool+" "+string(arguments))
		result := &mcp.CallToolResult{}
		switch tool {
		case "add_key":
			result.IsError = true
			result.Content = []mcp.Content{&mcp.TextContent{Text: "请粘贴一条有效的 SSH 公钥"}}
		case "open_service":
			result.Content = []mcp.Content{&mcp.TextContent{Text: `{"installed":true,"backend":"Running","forwards":[{"host":"100.88.2.4","port":8080,"address":"127.0.0.1:32123"}]}`}}
		case "logout":
			return nil, &hostbridge.RefusedError{Reason: errors.New("no")}
		case "disconnect":
			result.Content = []mcp.Content{&mcp.TextContent{Text: `{"installed":true,"backend":"NeedsLogin","auth_url":"https://evil.test/a/x"}`}}
		default:
			result.Content = []mcp.Content{&mcp.TextContent{Text: `{"installed":true,"backend":"NeedsLogin","auth_url":"https://login.tailscale.com/a/test"}`}}
		}
		return result, nil
	}
	if w := request(s, "POST", "/api/tailscale/connect", s.origin, "{}", true); w.Code != 200 || !strings.Contains(w.Body.String(), "login.tailscale.com") {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/tailscale/add-key", s.origin, `{"key":"invalid"}`, true); w.Code != 502 || !strings.Contains(w.Body.String(), "有效的 SSH 公钥") {
		t.Fatalf("key error: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/tailscale/open-service", s.origin, `{"host":"100.88.2.4","port":8080}`, true); w.Code != 200 || !strings.Contains(w.Body.String(), "127.0.0.1:32123") {
		t.Fatalf("service: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/tailscale/logout", s.origin, "{}", true); w.Code != 403 {
		t.Fatalf("an action not let through: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/tailscale/disconnect", s.origin, "{}", true); w.Code != 502 {
		t.Fatalf("a sign-in address from elsewhere was passed on: %d %s", w.Code, w.Body.String())
	}
	want := []string{`connect {}`, `add_key {"key":"invalid"}`, `open_service {"host":"100.88.2.4","port":8080}`, `logout {}`, `disconnect {}`}
	if strings.Join(called, "|") != strings.Join(want, "|") {
		t.Fatalf("called %v", called)
	}
	for _, body := range []string{`{"host":"100.88.2.4","port":0}`, `{"host":"100.88.2.4","port":65536}`, `{"host":"100.88.2.4","port":"80"}`, `{"port":80}`} {
		if w := request(s, "POST", "/api/tailscale/open-service", s.origin, body, true); w.Code != 400 {
			t.Fatalf("bad input accepted: %d %s", w.Code, body)
		}
	}
}

// Until Tailscale is installed the panel is told so; installing registers
// the plugin once and remembers that the user asked for it.
func TestTailnetPluginIsRegisteredOnce(t *testing.T) {
	s := testServer(t)
	s.ready = true
	if w := request(s, "GET", "/api/tailscale", "", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), `"installed":false`) {
		t.Fatalf("not installed: %d %s", w.Code, w.Body.String())
	}
	if changed, err := s.ensureTailnetPlugin(); err != nil || !changed {
		t.Fatalf("register: %t %v", changed, err)
	}
	manifest, err := hostbridge.LoadManifest(s.tailnetPluginDir())
	if err != nil || manifest.Kind != hostbridge.KindGuest || manifest.ServerCommand()[1] != "mcp" {
		t.Fatalf("manifest %+v: %v", manifest, err)
	}
	if !s.hostGrantStore().allowed(hostGrantPlugin+tailnetPlugin, manifest.Permissions) {
		t.Fatal("the user's installing it was not remembered")
	}
	// Taken back by the user, it stays taken back.
	if err := s.hostGrantStore().revoke(hostGrantPlugin + tailnetPlugin); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ensureTailnetPlugin(); err != nil || changed {
		t.Fatalf("registered again: %t %v", changed, err)
	}
	if s.hostGrantStore().allowed(hostGrantPlugin+tailnetPlugin, manifest.Permissions) {
		t.Fatal("a grant the user took back was given again")
	}
	// Installed but not running: the panel says what to do, not "not installed".
	if w := request(s, "GET", "/api/tailscale", "", "", true); w.Code != 502 {
		t.Fatalf("not running: %d %s", w.Code, w.Body.String())
	}
}
