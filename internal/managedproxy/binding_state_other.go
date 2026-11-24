//go:build !windows

package managedproxy

import (
	"net"
	"slices"
)

func systemInterfaceSnapshot(name string) (int, []string, bool) {
	if name == "" {
		return 0, nil, false
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return 0, nil, false
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return 0, nil, false
	}
	var addrs []string
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil || ip.IsLinkLocalUnicast() || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		addrs = append(addrs, v4.String())
	}
	if len(addrs) == 0 {
		return 0, nil, false
	}
	slices.Sort(addrs)
	addrs = slices.Compact(addrs)
	return iface.Index, addrs, true
}
