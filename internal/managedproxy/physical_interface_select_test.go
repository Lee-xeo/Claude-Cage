package managedproxy

import (
	"reflect"
	"testing"
)

func TestWindowsUplinkAdaptersAcceptsHyperVManagementTwin(t *testing.T) {
	nic := []byte{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}
	list := []windowsAdapterInfo{
		{index: 4, name: "Ethernet", hardware: true, ipv4: false, mac: nic},
		{index: 12, name: "vEthernet (External)", metric: 15, hardware: false, ipv4: true, mac: nic},
		{index: 20, name: "sing-box", hardware: false, ipv4: true, mac: []byte{0x02, 0, 0, 0, 0, 1}},
		{index: 21, name: "VMware Network Adapter VMnet8", hardware: false, ipv4: true, mac: []byte{0x00, 0x50, 0x56, 0xc0, 0, 8}},
		{index: 22, name: "aTrustVNIC", hardware: false, ipv4: true},
	}
	want := map[uint32]windowsPhysicalAdapter{12: {name: "vEthernet (External)", metric: 15}}
	if got := windowsUplinkAdapters(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("uplinks = %+v, want %+v", got, want)
	}
}

func TestWindowsUplinkAdaptersKeepsHardwareAndRejectsUntwinnedVirtual(t *testing.T) {
	wifi := []byte{0x3c, 0x22, 0xfb, 0x01, 0x02, 0x03}
	list := []windowsAdapterInfo{
		{index: 7, name: "Wi-Fi", metric: 25, hardware: true, ipv4: true, mac: wifi},
		// A virtual adapter copying the MAC of a hardware adapter that still
		// has its own IPv4 is not a bridged management adapter.
		{index: 30, name: "Clone", hardware: false, ipv4: true, mac: wifi},
		{index: 31, name: "Zero MAC", hardware: false, ipv4: true, mac: make([]byte, 6)},
	}
	want := map[uint32]windowsPhysicalAdapter{7: {name: "Wi-Fi", metric: 25}}
	if got := windowsUplinkAdapters(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("uplinks = %+v, want %+v", got, want)
	}
	// Two bridged hardware adapters sharing one MAC are ambiguous.
	dup := []byte{0x00, 0x15, 0x5d, 9, 9, 9}
	ambiguous := []windowsAdapterInfo{
		{index: 1, name: "A", hardware: true, mac: dup},
		{index: 2, name: "B", hardware: true, mac: dup},
		{index: 3, name: "vEthernet", ipv4: true, mac: dup},
	}
	if got := windowsUplinkAdapters(ambiguous); len(got) != 0 {
		t.Fatalf("ambiguous twin accepted: %+v", got)
	}
}

func TestWindowsUplinkAdaptersHyperVSoftRouterFallback(t *testing.T) {
	list := []windowsAdapterInfo{
		{index: 14, name: "以太网", description: "Realtek PCIe GbE Family Controller", hardware: true, ipv4: false, mac: []byte{0x1c, 0x83, 0x41, 0xc5, 0xcf, 0xd7}},
		{index: 82, name: "vEthernet (OpenWrt-LAN)", description: "Hyper-V Virtual Ethernet Adapter #2", metric: 15, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x01, 0x00}},
		{index: 59, name: "vEthernet (Default Switch)", description: "Hyper-V Virtual Ethernet Adapter", metric: 5000, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x51, 0x01}},
		{index: 66, name: "Mihomo", description: "Meta Tunnel", metric: 0, hardware: false, ipv4: true},
		{index: 16, name: "Radmin VPN", description: "Famatech Radmin VPN Ethernet Adapter", metric: 256, hardware: false, ipv4: true, mac: []byte{0x02, 0x50, 0x38, 0xea, 0x7b, 0x93}},
		{index: 22, name: "aTrustVNIC", description: "Sangfor aTrust Virtual NIC", metric: 25, hardware: false, ipv4: true, mac: []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}},
	}
	want := map[uint32]windowsPhysicalAdapter{
		82: {name: "vEthernet (OpenWrt-LAN)", metric: 15},
		59: {name: "vEthernet (Default Switch)", metric: 5000},
	}
	if got := windowsUplinkAdapters(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("soft-router uplinks = %+v, want %+v", got, want)
	}

	// Physical NIC passed through via DDA (absent from host) still falls back to
	// the host Hyper-V virtual ethernet adapter.
	passthrough := []windowsAdapterInfo{
		{index: 82, name: "vEthernet (OpenWrt-LAN)", description: "Hyper-V Virtual Ethernet Adapter #3", metric: 15, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x01, 0x00}},
	}
	if got := windowsUplinkAdapters(passthrough); !reflect.DeepEqual(got, map[uint32]windowsPhysicalAdapter{82: {name: "vEthernet (OpenWrt-LAN)", metric: 15}}) {
		t.Fatalf("passthrough uplinks = %+v", got)
	}
}

func TestWindowsUplinkAdaptersFallbackSkippedWhenHardwareOrTwinExists(t *testing.T) {
	wifi := []byte{0x3c, 0x22, 0xfb, 0x01, 0x02, 0x03}
	withHardware := []windowsAdapterInfo{
		{index: 7, name: "Wi-Fi", description: "Intel Wi-Fi", metric: 25, hardware: true, ipv4: true, mac: wifi},
		{index: 82, name: "vEthernet (OpenWrt-LAN)", description: "Hyper-V Virtual Ethernet Adapter #2", metric: 15, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x01, 0x00}},
		{index: 59, name: "vEthernet (Default Switch)", description: "Hyper-V Virtual Ethernet Adapter", metric: 5000, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x51, 0x01}},
	}
	if got := windowsUplinkAdapters(withHardware); !reflect.DeepEqual(got, map[uint32]windowsPhysicalAdapter{7: {name: "Wi-Fi", metric: 25}}) {
		t.Fatalf("hardware present uplinks = %+v, want Wi-Fi only", got)
	}

	nic := []byte{0x00, 0x15, 0x5d, 0x01, 0x02, 0x03}
	withTwin := []windowsAdapterInfo{
		{index: 4, name: "Ethernet", description: "Realtek PCIe GbE", hardware: true, ipv4: false, mac: nic},
		{index: 12, name: "vEthernet (External)", description: "Hyper-V Virtual Ethernet Adapter", metric: 15, hardware: false, ipv4: true, mac: nic},
		{index: 82, name: "vEthernet (Internal)", description: "Hyper-V Virtual Ethernet Adapter #2", metric: 10, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x01, 0x00}},
	}
	if got := windowsUplinkAdapters(withTwin); !reflect.DeepEqual(got, map[uint32]windowsPhysicalAdapter{12: {name: "vEthernet (External)", metric: 15}}) {
		t.Fatalf("twin present uplinks = %+v, want twin only", got)
	}
}

func TestWindowsUplinkAdaptersFallbackRejectsNonHyperVAndInvalidAdapters(t *testing.T) {
	list := []windowsAdapterInfo{
		{index: 10, name: "sing-box", description: "Wintun Userspace Tunnel", hardware: false, ipv4: true},
		{index: 11, name: "aTrustVNIC", description: "Sangfor aTrust Virtual NIC", hardware: false, ipv4: true, mac: []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}},
		{index: 12, name: "TAP", description: "TAP-Windows Adapter V9", hardware: false, ipv4: true, mac: []byte{0x00, 0xff, 0x01, 0x02, 0x03, 0x04}},
		{index: 13, name: "VMnet8", description: "VMware Virtual Ethernet Adapter for VMnet8", hardware: false, ipv4: true, mac: []byte{0x00, 0x50, 0x56, 0xc0, 0x00, 0x08}},
		// Guest VM synthetic NIC uses a different driver description and stays excluded.
		{index: 14, name: "Ethernet", description: "Microsoft Hyper-V Network Adapter", hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x10, 0x20, 0x30}},
		{index: 15, name: "NoMAC", description: "Hyper-V Virtual Ethernet Adapter #2", hardware: false, ipv4: true},
		{index: 16, name: "MulticastMAC", description: "Hyper-V Virtual Ethernet Adapter #3", hardware: false, ipv4: true, mac: []byte{0x01, 0x15, 0x5d, 0x00, 0x01, 0x00}},
		{index: 17, name: "SpoofSuffix", description: "Hyper-V Virtual Ethernet Adapter Tunnel", hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x02, 0x00}},
		{index: 18, name: "EmptyNumber", description: "Hyper-V Virtual Ethernet Adapter #", hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x03, 0x00}},
		{index: 19, name: "BadNumber", description: "Hyper-V Virtual Ethernet Adapter #2a", hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x04, 0x00}},
		{index: 20, name: "NoIPv4", description: "Hyper-V Virtual Ethernet Adapter #4", hardware: false, ipv4: false, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x05, 0x00}},
	}
	if got := windowsUplinkAdapters(list); len(got) != 0 {
		t.Fatalf("unexpected fallback adapters accepted: %+v", got)
	}
}
