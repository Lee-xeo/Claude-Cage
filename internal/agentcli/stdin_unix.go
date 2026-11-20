//go:build !windows

package agentcli

import (
	"os"

	"golang.org/x/sys/unix"
)

// pendingInput reports, without blocking, whether f already holds data a
// caller wrote for this process. Terminals never count: an interactive
// prompt is not an input document.
func pendingInput(f *os.File) bool {
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return false
	}
	if info.Mode().IsRegular() {
		return info.Size() > 0
	}
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return false
	}
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(fds, 0); err != nil || n == 0 || fds[0].Revents&unix.POLLIN == 0 {
		return false
	}
	// Readable can also mean the writer closed an empty pipe; a read now
	// returns at once either way.
	var b [1]byte
	n, _ := f.Read(b[:])
	return n > 0
}
