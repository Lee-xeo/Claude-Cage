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
