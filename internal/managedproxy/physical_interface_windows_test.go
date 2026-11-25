package managedproxy

import (
	"context"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSelectWindowsPhysicalInterface(t *testing.T) {
	adapters := map[uint32]windowsPhysicalAdapter{
		3:  {name: "Ethernet", metric: 30},
		28: {name: "Wi-Fi", metric: 10},
	}
	route := func(index, metric uint32) windows.MibIpForwardRow2 {
		r := windows.MibIpForwardRow2{InterfaceIndex: index, Metric: metric}
		r.DestinationPrefix.Prefix.Family = windows.AF_INET
		*(*windows.RawSockaddrInet4)(unsafe.Pointer(&r.NextHop)) = windows.RawSockaddrInet4{
			Family: windows.AF_INET,
			Addr:   [4]byte{192, 168, 1, 1},
		}
		return r
	}

	virtual := route(9, 0)
	nonDefault := route(3, 0)
	nonDefault.DestinationPrefix.PrefixLength = 24
	onLink := route(28, 0)
	(*windows.RawSockaddrInet4)(unsafe.Pointer(&onLink.NextHop)).Addr = [4]byte{}

	if got := selectWindowsPhysicalInterface(adapters, []windows.MibIpForwardRow2{
		virtual, nonDefault, onLink, route(3, 1), route(28, 20),
	}); got != "Wi-Fi" {
		t.Fatalf("selected %q, want Wi-Fi with lower combined metric", got)
	}
	if got := selectWindowsPhysicalInterface(adapters, []windows.MibIpForwardRow2{
		route(28, 21), route(3, 1),
	}); got != "Ethernet" {
		t.Fatalf("selected %q, want Ethernet with lower interface index", got)
	}
	if got := selectWindowsPhysicalInterface(adapters, []windows.MibIpForwardRow2{
		virtual, nonDefault, onLink,
	}); got != "" {
		t.Fatalf("selected %q without a usable hardware default route", got)
	}
}

func TestSelectWindowsPhysicalInterfaceHyperVSoftRouter(t *testing.T) {
	list := []windowsAdapterInfo{
		{index: 14, name: "以太网", description: "Realtek PCIe GbE Family Controller", hardware: true, ipv4: false, mac: []byte{0x1c, 0x83, 0x41, 0xc5, 0xcf, 0xd7}},
		{index: 82, name: "vEthernet (OpenWrt-LAN)", description: "Hyper-V Virtual Ethernet Adapter #2", metric: 15, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x01, 0x00}},
		{index: 59, name: "vEthernet (Default Switch)", description: "Hyper-V Virtual Ethernet Adapter", metric: 5000, hardware: false, ipv4: true, mac: []byte{0x00, 0x15, 0x5d, 0x00, 0x51, 0x01}},
		{index: 66, name: "Mihomo", description: "Meta Tunnel", metric: 0, hardware: false, ipv4: true},
		{index: 16, name: "Radmin VPN", description: "Famatech Radmin VPN Ethernet Adapter", metric: 256, hardware: false, ipv4: true, mac: []byte{0x02, 0x50, 0x38, 0xea, 0x7b, 0x93}},
	}
	adapters := windowsUplinkAdapters(list)
	route := func(index, metric uint32, hop [4]byte) windows.MibIpForwardRow2 {
		r := windows.MibIpForwardRow2{InterfaceIndex: index, Metric: metric}
		r.DestinationPrefix.Prefix.Family = windows.AF_INET
		*(*windows.RawSockaddrInet4)(unsafe.Pointer(&r.NextHop)) = windows.RawSockaddrInet4{
			Family: windows.AF_INET,
			Addr:   hop,
		}
		return r
	}
	routes := []windows.MibIpForwardRow2{
		route(66, 0, [4]byte{198, 18, 0, 2}),  // Mihomo TUN
		route(82, 0, [4]byte{192, 168, 1, 1}), // OpenWrt-LAN
		route(16, 9000, [4]byte{26, 0, 0, 1}), // Radmin VPN
	}
	if got := selectWindowsPhysicalInterface(adapters, routes); got != "vEthernet (OpenWrt-LAN)" {
		t.Fatalf("selected %q, want vEthernet (OpenWrt-LAN)", got)
	}
	// Without a default route on any Hyper-V vEthernet, Default Switch alone
	// cannot become the uplink.
	if got := selectWindowsPhysicalInterface(adapters, []windows.MibIpForwardRow2{
		route(66, 0, [4]byte{198, 18, 0, 2}),
		route(16, 9000, [4]byte{26, 0, 0, 1}),
	}); got != "" {
		t.Fatalf("selected %q without a Hyper-V default route", got)
	}
}

func TestWindowsForwardTableLayout(t *testing.T) {
	if size := unsafe.Sizeof(windows.MibIpForwardRow2{}); size != 104 {
		t.Fatalf("unexpected MIB_IPFORWARD_ROW2 size: %d", size)
	}
	if offset := unsafe.Offsetof(windows.MibIpForwardTable2{}.Table); offset != 8 {
		t.Fatalf("unexpected offset of MIB_IPFORWARD_TABLE2.Table: %d", offset)
	}
}

func BenchmarkWindowsPhysicalInterface(b *testing.B) {
	ctx := context.Background()
	name, err := windowsPhysicalInterface(ctx)
	if err != nil {
		b.Skip(err)
	}
	b.Logf("selected physical interface: %s", name)
	for b.Loop() {
		if got, err := windowsPhysicalInterface(ctx); err != nil || got != name {
			b.Fatalf("selected %q (%v), expected %q", got, err, name)
		}
	}
}
