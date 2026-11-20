package managedproxy

import "bytes"

type windowsPhysicalAdapter struct {
	name   string
	metric uint32
}

// windowsAdapterInfo is what the IP Helper reports for one Up adapter.
type windowsAdapterInfo struct {
	index    uint32
	name     string
	metric   uint32
	hardware bool   // MIB_IF_ROW2 HardwareInterface
	ipv4     bool   // has a preferred, routable IPv4 address
	mac      []byte // permanent or current link-layer address
}

// windowsUplinkAdapters keeps hardware adapters that carry IPv4. When a
// hardware adapter is bound to a Hyper-V external switch it has no IPv4 of its
// own; the host's address sits on the management vEthernet adapter, which
// reuses the hardware adapter's MAC. Only such an exact twin of an Up hardware
// adapter without IPv4 is accepted. VPN, TUN, NAT and other virtual adapters
// have no hardware twin and stay excluded.
func windowsUplinkAdapters(list []windowsAdapterInfo) map[uint32]windowsPhysicalAdapter {
	var bridged [][]byte
	for _, a := range list {
		if a.hardware && !a.ipv4 && validMAC(a.mac) {
			bridged = append(bridged, a.mac)
		}
	}
	out := map[uint32]windowsPhysicalAdapter{}
	for _, a := range list {
		if a.index == 0 || a.name == "" || !a.ipv4 {
			continue
		}
		accept := a.hardware
		if !accept && validMAC(a.mac) {
			matches := 0
			for _, mac := range bridged {
				if bytes.Equal(mac, a.mac) {
					matches++
				}
			}
			accept = matches == 1
		}
		if accept {
			out[a.index] = windowsPhysicalAdapter{name: a.name, metric: a.metric}
		}
	}
	return out
}

func validMAC(mac []byte) bool {
	return len(mac) == 6 && !bytes.Equal(mac, make([]byte, 6)) && mac[0]&1 == 0
}
