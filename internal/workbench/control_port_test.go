package workbench

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// Reserved or occupied preferred ports must not stop the controller (#179).
func TestListenControllerFallsBackWhenPreferredPortsAreTaken(t *testing.T) {
	var held []net.Listener
	defer func() {
		for _, l := range held {
			l.Close()
		}
	}()
	for port := controllerFirstPort; port < controllerFirstPort+controllerPortCount; port++ {
		l, err := listenLoopback(port)
		if err != nil {
			continue // Already unavailable, which is the condition under test.
		}
		held = append(held, l)
	}
	profile := t.TempDir()
	first, port, err := listenController(profile)
	if err != nil {
		t.Fatal(err)
	}
	if port >= controllerFirstPort && port < controllerFirstPort+controllerPortCount {
		t.Fatalf("listener used preferred port %d although all were held", port)
	}
	var record controllerPortRecord
	b, err := os.ReadFile(filepath.Join(profile, "control-port.json"))
	if err != nil || json.Unmarshal(b, &record) != nil || record.Port != port {
		t.Fatalf("fallback port was not remembered: %s %v", b, err)
	}
	first.Close()
	second, again, err := listenController(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if again != port {
		t.Fatalf("restart moved the origin from port %d to %d", port, again)
	}
}

func TestListenControllerPrefersStablePortRange(t *testing.T) {
	profile := t.TempDir()
	l, port, err := listenController(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if port >= controllerFirstPort && port < controllerFirstPort+controllerPortCount {
		if _, err := os.Stat(filepath.Join(profile, "control-port.json")); !os.IsNotExist(err) {
			t.Fatalf("preferred port wrote a fallback record: %v", err)
		}
	}
}
