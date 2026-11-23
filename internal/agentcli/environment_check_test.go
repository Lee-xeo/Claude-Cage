package agentcli

import (
	"encoding/json"
	"testing"
)

func TestEnvironmentCheckReportsClosedNetwork(t *testing.T) {
	for _, c := range []struct{ status, code, next string }{
		{`{"ready":true,"vm_running":true,"network_open":true,"safety":{"gate":{"mode":"open"}}}`, "environment_ready", ""},
		{`{"ready":true,"vm_running":true}`, "environment_ready", ""},
		{`{"ready":true,"vm_running":true,"network_paused":true,"network_open":false}`, "network_paused", "network.check"},
		{`{"ready":true,"vm_running":true,"network_open":false,"safety":{"gate":{"mode":"checking"}}}`, "network_checking", "status"},
		{`{"ready":true,"vm_running":true,"network_open":false,"safety":{"gate":{"mode":"closed"}}}`, "network_closed", "network.check"},
		{`{"ready":true,"vm_running":true,"network_open":false,"network_error":"proxy down"}`, "network_not_ready", "inspect_network_policy"},
		{`{"ready":false,"vm_running":false,"network_paused":true,"network_open":false}`, "environment_not_ready", "environment.start"},
	} {
		code, _, next := environmentCheck(json.RawMessage(c.status))
		if code != c.code || next != c.next {
			t.Errorf("%s: got %s/%s, want %s/%s", c.status, code, next, c.code, c.next)
		}
	}
}
