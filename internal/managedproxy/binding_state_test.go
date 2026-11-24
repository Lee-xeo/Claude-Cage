package managedproxy

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mockBindingHooks(t *testing.T, snap func(string) (int, []string, bool), uplink func(context.Context) (string, error)) {
	t.Helper()
	prevSnap, prevUplink := snapshotInterfaceState, currentPhysicalUplink
	t.Cleanup(func() {
		snapshotInterfaceState = prevSnap
		currentPhysicalUplink = prevUplink
	})
	if snap != nil {
		snapshotInterfaceState = snap
	}
	if uplink != nil {
		currentPhysicalUplink = uplink
	}
}

func TestBindingSnapshotAndAddressDrift(t *testing.T) {
	type nicState struct {
		index int
		addrs []string
		ok    bool
	}
	nics := map[string]nicState{
		"WLAN": {index: 12, addrs: []string{"192.168.1.20"}, ok: true},
	}
	uplink := "WLAN"
	var uplinkErr error
	mockBindingHooks(
		t,
		func(name string) (int, []string, bool) {
			s := nics[name]
			return s.index, append([]string(nil), s.addrs...), s.ok
		},
		func(context.Context) (string, error) {
			if uplinkErr != nil {
				return "", uplinkErr
			}
			return uplink, nil
		},
	)

	profile := t.TempDir()
	configPath := filepath.Join(Dir(profile), "config.yaml")
	data, err := BindInterface([]byte("proxies:\n  - {name: exit, type: socks5, server: 203.0.113.10, port: 1080}\n"), "WLAN")
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(configPath, data); err != nil {
		t.Fatal(err)
	}

	// Missing sidecar stays backward-compatible.
	if !BindingMatches(configPath, "WLAN") || BindingAddressChanged(configPath, "WLAN") {
		t.Fatal("missing interface-binding.json should remain compatible with YAML binding")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Drifted() || status.Bound != "WLAN" || status.Current != "WLAN" {
		t.Fatalf("expected healthy binding without sidecar, got %+v", status)
	}

	// Save snapshot and verify exact match.
	saveInterfaceBindingState(configPath, data, "WLAN")
	if !BindingMatches(configPath, "WLAN") || BindingAddressChanged(configPath, "WLAN") {
		t.Fatal("freshly saved binding state did not match")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Drifted() || status.Message() != "" {
		t.Fatalf("expected no drift, got %+v", status)
	}

	// Same interface name with changed IPv4 (DHCP / Wi-Fi switch).
	nics["WLAN"] = nicState{index: 12, addrs: []string{"10.20.30.40"}, ok: true}
	if BindingMatches(configPath, "WLAN") || !BindingAddressChanged(configPath, "WLAN") {
		t.Fatal("IPv4 change on same interface was not detected")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "address_changed" || !strings.Contains(status.Message(), "IP 地址或接口状态已变化") {
		t.Fatalf("expected address_changed for IPv4 change, got %+v (%q)", status, status.Message())
	}

	// Restore IPv4, change interface index (USB NIC replug).
	nics["WLAN"] = nicState{index: 19, addrs: []string{"192.168.1.20"}, ok: true}
	if BindingMatches(configPath, "WLAN") || !BindingAddressChanged(configPath, "WLAN") {
		t.Fatal("interface index change on same interface was not detected")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "address_changed" {
		t.Fatalf("expected address_changed for index change, got %+v", status)
	}

	// Same interface goes down / loses usable IPv4.
	nics["WLAN"] = nicState{ok: false}
	if BindingMatches(configPath, "WLAN") || !BindingAddressChanged(configPath, "WLAN") {
		t.Fatal("down interface was not detected")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "address_changed" {
		t.Fatalf("expected address_changed for down interface, got %+v", status)
	}

	// Different physical uplink name.
	nics["WLAN"] = nicState{index: 12, addrs: []string{"192.168.1.20"}, ok: true}
	uplink = "Ethernet"
	if BindingMatches(configPath, "Ethernet") || BindingAddressChanged(configPath, "Ethernet") {
		t.Fatal("interface rename/switch was misclassified")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "interface_changed" || !strings.Contains(status.Message(), "Ethernet") || !strings.Contains(status.Message(), "cage proxy-start") {
		t.Fatalf("expected interface_changed, got %+v (%q)", status, status.Message())
	}
	uplink = "WLAN"

	// Uplink unavailable.
	uplinkErr = errors.New("no uplink")
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "uplink_unavailable" || !strings.Contains(status.Message(), "物理 IPv4 网卡") {
		t.Fatalf("expected uplink_unavailable, got %+v (%q)", status, status.Message())
	}
	uplinkErr = nil

	// Corrupt sidecar fails closed.
	if err := os.WriteFile(bindingStatePath(configPath), []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if BindingMatches(configPath, "WLAN") || !BindingAddressChanged(configPath, "WLAN") {
		t.Fatal("corrupt interface-binding.json did not fail closed")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "address_changed" {
		t.Fatalf("expected address_changed for corrupt sidecar, got %+v", status)
	}

	// External config rewrite invalidates sidecar SHA256 and falls back to YAML check.
	saveInterfaceBindingState(configPath, data, "WLAN")
	rewritten, err := BindInterface([]byte("proxies:\n  - {name: other, type: socks5, server: 203.0.113.11, port: 1080}\n"), "WLAN")
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(configPath, rewritten); err != nil {
		t.Fatal(err)
	}
	if !BindingMatches(configPath, "WLAN") {
		t.Fatal("stale ConfigSHA256 sidecar should fall back cleanly to YAML check")
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Drifted() {
		t.Fatalf("expected healthy fallback after config rewrite, got %+v", status)
	}

	// Removing sidecar via removeInterfaceBindingState removes the file.
	saveInterfaceBindingState(configPath, rewritten, "WLAN")
	if _, err := os.Stat(bindingStatePath(configPath)); err != nil {
		t.Fatal(err)
	}
	removeInterfaceBindingState(profile)
	if _, err := os.Stat(bindingStatePath(configPath)); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar removal, err=%v", err)
	}
}

func TestCheckRuntimeBindingRouteMissingAndLocalNode(t *testing.T) {
	profile := t.TempDir()
	mockBindingHooks(
		t,
		func(string) (int, []string, bool) { return 1, []string{"192.168.1.50"}, true },
		func(context.Context) (string, error) { return "WLAN", nil },
	)

	// Missing config.yaml.
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "missing_config" || !strings.Contains(status.Message(), "cage proxy-start") {
		t.Fatalf("expected missing_config, got %+v", status)
	}

	// Local proxy node.
	configPath := filepath.Join(Dir(profile), "config.yaml")
	if err := Write(configPath, []byte("interface-name: WLAN\nproxies:\n  - {name: local, type: socks5, server: 127.0.0.1, port: 10808}\n")); err != nil {
		t.Fatal(err)
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "local_node" || status.Message() != ErrLocalProxyNode.Error() {
		t.Fatalf("expected local_node, got %+v", status)
	}

	// Per-node route interface drift.
	prevList, prevProbe := listHostInterfaces, probeRouteSourceIP
	t.Cleanup(func() {
		listHostInterfaces = prevList
		probeRouteSourceIP = prevProbe
	})
	listHostInterfaces = func() ([]hostInterface, error) {
		return []hostInterface{
			{name: "WLAN", prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.50/24")}},
			{name: "Tailscale", prefixes: []netip.Prefix{netip.MustParsePrefix("100.100.1.5/32")}},
		}, nil
	}
	probeRouteSourceIP = func(dst netip.Addr) (netip.Addr, bool) {
		if tailscaleIPv4Prefix.Contains(dst) {
			return netip.MustParseAddr("100.100.1.5"), true
		}
		return netip.MustParseAddr("192.168.1.50"), true
	}
	bound, err := BindInterface([]byte("proxies:\n  - {name: ts, type: socks5, server: 100.88.2.4, port: 1080}\n"), "WLAN")
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(configPath, bound); err != nil {
		t.Fatal(err)
	}
	// Disconnect Tailscale so route_changed triggers while physical interface stays WLAN.
	listHostInterfaces = func() ([]hostInterface, error) {
		return []hostInterface{
			{name: "WLAN", prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.50/24")}},
		}, nil
	}
	probeRouteSourceIP = func(netip.Addr) (netip.Addr, bool) {
		return netip.MustParseAddr("192.168.1.50"), true
	}
	if status := CheckRuntimeBinding(context.Background(), profile); status.Reason != "route_changed" || !strings.Contains(status.Message(), "路由接口已变化") {
		t.Fatalf("expected route_changed, got %+v (%q)", status, status.Message())
	}
}
