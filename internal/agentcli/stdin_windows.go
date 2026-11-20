package agentcli

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var peekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

// pendingInput reports, without blocking, whether f already holds data a
// caller wrote for this process. Consoles never count.
func pendingInput(f *os.File) bool {
	handle := windows.Handle(f.Fd())
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return false
	}
	switch kind {
	case windows.FILE_TYPE_DISK:
		info, err := f.Stat()
		return err == nil && info.Size() > 0
	case windows.FILE_TYPE_PIPE:
		var available uint32
		ok, _, _ := peekNamedPipe.Call(uintptr(handle), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
		return ok != 0 && available > 0
	}
	return false
}
