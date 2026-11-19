package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

func validTailnetAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "login.tailscale.com" && u.User == nil && u.Fragment == "" && (len(u.Path) > 3 && (u.Path[:3] == "/a/" || u.Path[:3] == "/c/"))
}
func (s *Server) tailnet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	action := r.PathValue("action")
	if r.Method == http.MethodGet {
		action = "status"
	}
	if action == "install" {
		var input struct{}
		if !readJSON(w, r, &input) {
			return
		}
		s.launch(w, "tailscale-install", s.installTailnetNow)
		return
	}
	switch action {
	case "status", "connect", "login", "disconnect", "logout", "add-key", "remove-key", "open-service":
	default:
		http.NotFound(w, r)
		return
	}
	var input struct {
		Key  string `json:"key"`
		Host string `json:"host,omitempty"`
		Port int    `json:"port,omitempty"`
	}
	if r.Method != http.MethodGet && !readJSON(w, r, &input) {
		return
	}
	if len(input.Key) > 8192 {
		http.Error(w, "SSH 公钥过长", 400)
		return
	}
	if action == "open-service" && (len(input.Host) == 0 || len(input.Host) > 253 || input.Port < 1 || input.Port > 65535) {
		http.Error(w, "请选择私网设备并填写 1–65535 之间的端口", 400)
		return
	}
	s.mu.Lock()
	ready := s.ready
	busy := s.jobs.snapshot().Running
	s.mu.Unlock()
	if !ready || busy {
		http.Error(w, "请先启动 Cage 环境，并等待当前操作完成", 409)
		return
	}
	// Not installed: the panel offers to install it.
	if s.tailnetTool == nil && !s.tailnetPluginInstalled() {
		jsonReply(w, map[string]any{"installed": false, "enabled": false, "backend": "NotInstalled", "ips": []string{}, "peers": []string{}, "keys": []string{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	arguments, _ := json.Marshal(struct {
		Key  string `json:"key,omitempty"`
		Host string `json:"host,omitempty"`
		Port int    `json:"port,omitempty"`
	}{input.Key, input.Host, input.Port})
	called, err := s.callTailnet(ctx, tailnetTools[action], arguments)
	var refused *hostbridge.RefusedError
	switch {
	case errors.As(err, &refused):
		http.Error(w, "这次 Tailscale 操作没有获得允许", http.StatusForbidden)
		return
	case err != nil || called == nil:
		http.Error(w, "Tailscale 操作未完成。请在「MCP 插件」里确认 Tailscale 已启用并重新加载；原设备身份和 SSH 公钥会保留。", 502)
		return
	}
	text := ""
	if len(called.Content) > 0 {
		if content, ok := called.Content[0].(*mcp.TextContent); ok {
			text = content.Text
		}
	}
	// What the action itself reports, such as a key that is not one.
	if called.IsError {
		http.Error(w, text, 502)
		return
	}
	data := []byte(text)
	var result map[string]json.RawMessage
	if json.Unmarshal(data, &result) != nil {
		http.Error(w, "Tailscale 组件返回了无效状态", 502)
		return
	}
	var authURL string
	_ = json.Unmarshal(result["auth_url"], &authURL)
	if authURL != "" && !validTailnetAuthURL(authURL) {
		http.Error(w, "Tailscale 登录地址无效，请重新发起登录", 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
