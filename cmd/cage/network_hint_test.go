package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNetworkHint(t *testing.T) {
	for _, c := range []struct{ name, status, want string }{
		{"paused", `{"ready":true,"vm_running":true,"network_paused":true,"network_open":false}`, "paused"},
		{"checking", `{"ready":true,"vm_running":true,"network_open":false,"safety":{"gate":{"mode":"checking"}}}`, "still running"},
		{"closed", `{"ready":true,"vm_running":true,"network_open":false,"safety":{"gate":{"mode":"closed"}}}`, "network.check"},
		{"open", `{"ready":true,"vm_running":true,"network_open":true}`, ""},
		{"stopped", `{"ready":false,"vm_running":false,"network_open":false}`, ""},
		{"older workbench", `{"ready":true,"vm_running":true}`, ""},
		{"invalid", `[]`, ""},
	} {
		got := networkHint(json.RawMessage(c.status))
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
