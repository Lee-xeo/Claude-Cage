package managedproxy

import (
	"context"
	"strings"
	"testing"
)

func TestDetectWindowsHostTUNConflict(t *testing.T) {
	wifiMAC := []byte{0x3c, 0x22, 0xfb, 0x01, 0x02, 0x03}
	nicMAC := []byte{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}

	t.Run("clash tun 0/0 route without physical forwarding", func(t *testing.T) {
		adapters := []windowsDiagnosticAdapter{
			{index: 7, name: "WLAN 2", hardware: true, ipv4: true, mac: wifiMAC},
			{index: 66, name: "Mihomo", description: "Meta Tunnel", hardware: false, ipv4: true},
		}
		routes := []windowsDiagnosticRoute{
			{interfaceIndex: 7, prefixLen: 0},
			{interfaceIndex: 66, prefixLen: 0},
		}
		got := detectWindowsHostTUNConflict(adapters, routes, "WLAN 2")
		if !got.ActiveHostTUN || got.PhysicalForwarding {
			t.Fatalf("unexpected conflict detection: %+v", got)
		}
		hint := got.FormatHint()
		if !strings.Contains(hint, "Clash TUN") || strings.Contains(hint, "IPv4 转发") {
			t.Fatalf("unexpected hint: %q", hint)
		}
	})

	t.Run("split default tun with physical forwarding enabled", func(t *testing.T) {
		adapters := []windowsDiagnosticAdapter{
			{index: 7, name: "WLAN", hardware: true, ipv4: true, mac: wifiMAC, forwardingEnabled: true},
			{index: 42, name: "Wintun", hardware: false, ipv4: true},
		}
		routes := []windowsDiagnosticRoute{
			{interfaceIndex: 7, prefixLen: 0},
			{interfaceIndex: 42, prefixLen: 1, prefix: [4]byte{0, 0, 0, 0}},
			{interfaceIndex: 42, prefixLen: 1, prefix: [4]byte{128, 0, 0, 0}},
		}
		got := detectWindowsHostTUNConflict(adapters, routes, "WLAN")
		if !got.ActiveHostTUN || !got.PhysicalForwarding {
			t.Fatalf("unexpected conflict detection: %+v", got)
		}
		hint := got.FormatHint()
		if !strings.Contains(hint, "IPv4 转发") || !strings.Contains(hint, "Hysteria2/TUIC") {
			t.Fatalf("unexpected forwarding hint: %q", hint)
		}
	})

	t.Run("physical forwarding without host tun yields empty hint", func(t *testing.T) {
		adapters := []windowsDiagnosticAdapter{
			{index: 7, name: "WLAN", hardware: true, ipv4: true, mac: wifiMAC, forwardingEnabled: true},
			{index: 20, name: "VMware Network Adapter VMnet8", hardware: false, ipv4: true, mac: []byte{0x00, 0x50, 0x56, 0xc0, 0, 8}},
		}
		routes := []windowsDiagnosticRoute{
			{interfaceIndex: 7, prefixLen: 0},
		}
		got := detectWindowsHostTUNConflict(adapters, routes, "WLAN")
		if got.ActiveHostTUN || got.FormatHint() != "" {
			t.Fatalf("expected empty hint when no host TUN is active, got %+v (%q)", got, got.FormatHint())
		}
	})

	t.Run("hyper-v external switch twin carrying default route is not treated as tun", func(t *testing.T) {
		adapters := []windowsDiagnosticAdapter{
			{index: 4, name: "Ethernet", hardware: true, ipv4: false, mac: nicMAC},
			{index: 12, name: "vEthernet (External)", hardware: false, ipv4: true, mac: nicMAC},
			{index: 59, name: "vEthernet (Default Switch)", hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x51, 0x01}},
		}
		routes := []windowsDiagnosticRoute{
			{interfaceIndex: 12, prefixLen: 0},
		}
		got := detectWindowsHostTUNConflict(adapters, routes, "vEthernet (External)")
		if got.ActiveHostTUN || got.FormatHint() != "" {
			t.Fatalf("Hyper-V external switch twin was misclassified as TUN: %+v", got)
		}
	})

	t.Run("virtual adapters without default route yield empty hint", func(t *testing.T) {
		adapters := []windowsDiagnosticAdapter{
			{index: 7, name: "WLAN", hardware: true, ipv4: true, mac: wifiMAC},
			{index: 42, name: "Wintun", hardware: false, ipv4: true},
			{index: 59, name: "vEthernet (Default Switch)", hardware: false, ipv4: true},
		}
		routes := []windowsDiagnosticRoute{
			{interfaceIndex: 7, prefixLen: 0},
			{interfaceIndex: 42, prefixLen: 1, prefix: [4]byte{0, 0, 0, 0}}, // only half of split default
		}
		got := detectWindowsHostTUNConflict(adapters, routes, "WLAN")
		if got.ActiveHostTUN || got.FormatHint() != "" {
			t.Fatalf("partial virtual route misclassified as TUN: %+v", got)
		}
	})

	t.Run("live windows hint call stays non-panicking", func(t *testing.T) {
		_ = WindowsNetworkConflictHint(context.Background(), "")
	})
}
