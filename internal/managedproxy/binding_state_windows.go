//go:build windows

package managedproxy

import (
	"context"

	"golang.org/x/sys/windows"
)

func systemInterfaceSnapshot(name string) (int, []string, bool) {
	if name == "" {
		return 0, nil, false
	}
	var index int
	var addrs []string
	var found bool
	err := visitWindowsAdapters(context.Background(), func(adapter *windows.IpAdapterAddresses) bool {
		if adapter.IfIndex == 0 || adapter.OperStatus != windows.IfOperStatusUp || adapter.FriendlyName == nil {
			return true
		}
		if windows.UTF16PtrToString(adapter.FriendlyName) != name {
			return true
		}
		row := windows.MibIfRow2{InterfaceIndex: adapter.IfIndex}
		if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row); err != nil || row.OperStatus != windows.IfOperStatusUp {
			return true
		}
		v4 := preferredWindowsIPv4Addrs(adapter.FirstUnicastAddress)
		if len(v4) == 0 {
			return true
		}
		index, addrs, found = int(adapter.IfIndex), v4, true
		return false
	})
	if err != nil {
		return 0, nil, false
	}
	return index, addrs, found
}
