package workbench

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexAppApproval(t *testing.T) {
	const frame = `{"id":17,"method":"mcpServer/elicitation/request","params":{"serverName":"cua_repl","threadId":"thread","turnId":"turn","mode":"form","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call","connector_id":"computer-use","tool_name":"get_app_state","tool_params":{"app":"com.apple.calculator"},"persist":["session","always"]}}}`
	for _, tc := range []struct {
		old, new      string
		grant, prompt bool
	}{
		{"", "", true, true}, {"", "", false, true}, {"cua_repl", "untrusted", true, false}, {`"turnId":"turn"`, `"turnId":"previous"`, true, false}, {`"threadId":"thread"`, `"threadId":"other"`, true, false}, {"get_app_state", "start_process", true, false}, {`"app":"com.apple.calculator"`, `"app":"com.apple.calculator","command":"test"`, true, false}, {`"properties":{}`, `"properties":{"credential":{}}`, true, false},
	} {
		raw := frame
		if tc.old != "" {
			raw = strings.Replace(raw, tc.old, tc.new, 1)
		}
		var event codexEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		prompted := false
		session := codexConversation{thread: "thread", turn: "turn", approve: func(r codexAppRequest) bool { prompted = true; return tc.grant }}
		result := session.requestReply(event)["result"].(map[string]any)
		if prompted != tc.prompt || (result["action"] == "accept") != (tc.prompt && tc.grant) {
			t.Fatalf("change %q: prompt=%v result=%v", tc.old, prompted, result)
		}
	}
}
