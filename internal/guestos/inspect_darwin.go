package guestos

import (
	"context"
	"runtime"

	"golang.org/x/sys/unix"
)

// A Mac host runs the shared Debian workbench through the system hypervisor.
func Inspect(_ context.Context) Inspection {
	hv, err := unix.SysctlUint32("kern.hv_support")
	return macInspection(runtime.GOARCH, err == nil && hv == 1)
}
