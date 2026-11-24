//go:build !windows

package managedproxy

import "context"

// WindowsNetworkConflictHint reports Windows-specific host TUN/forwarding
// conflicts; it returns "" on non-Windows hosts.
func WindowsNetworkConflictHint(context.Context, string) string {
	return ""
}
