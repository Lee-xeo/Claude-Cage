package guestos

import (
	"context"
	"io"
	"runtime"

	"github.com/sylvinus/agent-vm/internal/host"
	"golang.org/x/sys/windows"
)

// A Windows host runs the shared Debian workbench through WHPX and QEMU.
func Inspect(_ context.Context) Inspection {
	return windowsInspection(runtime.GOARCH, windows.RtlGetVersion().BuildNumber, func(w io.Writer) bool {
		return host.WindowsPrereqs(io.Discard, w)
	})
}
