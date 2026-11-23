package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sylvinus/agent-vm/internal/workbench"
)

// A guest connection refused by the closed egress gate does not say why, so
// guest commands first name the closed network and how to reopen it (#761).
func warnClosedNetwork(configPath string, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := workbench.AgentCall(ctx, configPath, "status", nil)
	if err != nil {
		// Without a reachable workbench there is no gate state to report.
		return
	}
	if hint := networkHint(status); hint != "" {
		fmt.Fprintln(stderr, "cage: "+hint)
	}
}

const networkCheckCommand = "cage agent call network.check --wait 5m"

func networkHint(status json.RawMessage) string {
	var view struct {
		Ready         bool  `json:"ready"`
		Running       bool  `json:"vm_running"`
		NetworkPaused bool  `json:"network_paused"`
		NetworkOpen   *bool `json:"network_open"`
		Safety        struct {
			Gate *struct {
				Mode string `json:"mode"`
			} `json:"gate"`
		} `json:"safety"`
	}
	if json.Unmarshal(status, &view) != nil || !view.Ready || !view.Running || view.NetworkOpen == nil || *view.NetworkOpen {
		return ""
	}
	switch {
	case view.NetworkPaused:
		return "external networking is paused, so guest connections are refused. Run '" + networkCheckCommand + "' or check the network again in Cage to resume it."
	case view.Safety.Gate != nil && view.Safety.Gate.Mode == "checking":
		return "the network check is still running; guest connections are refused until it passes. Run 'cage agent call status' to follow it."
	}
	return "external networking is closed until the network check passes, so guest connections are refused. Run '" + networkCheckCommand + "' or check the network in Cage."
}
