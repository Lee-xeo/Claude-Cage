//go:build windows

package managedproxy

import (
	"context"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WindowsNetworkConflictHint inspects the host routing table and bound adapter
// forwarding setting on Windows and returns a diagnostic hint when an active
// host TUN/VPN default route is present. Returns "" when no conflict is seen or
// when system queries fail.
func WindowsNetworkConflictHint(ctx context.Context, boundInterface string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var adapters []windowsDiagnosticAdapter
	err := visitWindowsAdapters(ctx, func(adapter *windows.IpAdapterAddresses) bool {
		if adapter.IfIndex == 0 || adapter.OperStatus != windows.IfOperStatusUp {
			return true
		}
		row := windows.MibIfRow2{InterfaceIndex: adapter.IfIndex}
		if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormalWithoutStatistics, &row); err != nil ||
			row.OperStatus != windows.IfOperStatusUp {
			return true
		}
		info := windowsDiagnosticAdapter{
			index:    adapter.IfIndex,
			metric:   adapter.Ipv4Metric,
			hardware: row.InterfaceAndOperStatusFlags&windowsHardwareInterface != 0,
			ipv4:     hasPreferredWindowsIPv4(adapter.FirstUnicastAddress),
		}
		if n := min(int(adapter.PhysicalAddressLength), len(adapter.PhysicalAddress)); n > 0 {
			info.mac = append([]byte(nil), adapter.PhysicalAddress[:n]...)
		}
		if adapter.FriendlyName != nil {
			info.name = windows.UTF16PtrToString(adapter.FriendlyName)
		}
		if adapter.Description != nil {
			info.description = windows.UTF16PtrToString(adapter.Description)
		}
		if boundInterface != "" && info.name == boundInterface {
			ipRow := windows.MibIpInterfaceRow{
				Family:         windows.AF_INET,
				InterfaceIndex: adapter.IfIndex,
			}
			if err := windows.GetIpInterfaceEntry(&ipRow); err == nil {
				info.forwardingEnabled = ipRow.ForwardingEnabled != 0
			}
		}
		adapters = append(adapters, info)
		return true
	})
	if err != nil || len(adapters) == 0 || ctx.Err() != nil {
		return ""
	}
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil || table == nil {
		return ""
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	if ctx.Err() != nil {
		return ""
	}
	var routes []windowsDiagnosticRoute
	for _, r := range table.Rows() {
		if r.DestinationPrefix.Prefix.Family != windows.AF_INET || r.DestinationPrefix.PrefixLength > 1 {
			continue
		}
		addr := (*windows.RawSockaddrInet4)(unsafe.Pointer(&r.DestinationPrefix.Prefix))
		routes = append(routes, windowsDiagnosticRoute{
			interfaceIndex: r.InterfaceIndex,
			prefixLen:      r.DestinationPrefix.PrefixLength,
			prefix:         addr.Addr,
			loopback:       r.Loopback != 0,
		})
	}
	return detectWindowsHostTUNConflict(adapters, routes, boundInterface).FormatHint()
}
