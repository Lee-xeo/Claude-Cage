//go:build darwin && arm64 && cgo && !no_vz

package maclogin

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework Virtualization
#include "bridge.h"
*/
import "C"

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"
	"unsafe"

	apple "github.com/Code-Hex/vz/v3"
	driver "github.com/lima-vm/lima/v2/pkg/driver/vz"
)

type objectReference interface{ CageViewObject() unsafe.Pointer }

func init() {
	if _, built := any((*apple.VirtualMachine)(nil)).(objectReference); !built {
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "hostagent" || os.Args[1] == "--debug") {
		runtime.LockOSThread()
	}
	driver.OwnsOffscreenView = ownsLoginDirectory
	driver.OffscreenView = runVZ
}

type vzSurface struct{}

func (vzSurface) Enable(v bool)       { C.cageLoginEnable(C.bool(v)) }
func (vzSurface) Click(x, y int) bool { return bool(C.cageLoginClick(C.int(x), C.int(y))) }
func (vzSurface) Key(code int, shift bool) bool {
	return bool(C.cageLoginKey(C.int(code), C.bool(shift)))
}
func (vzSurface) Scroll(x, y, dx, dy int) bool {
	return bool(C.cageLoginScroll(C.int(x), C.int(y), C.int(dx), C.int(dy)))
}
func (vzSurface) SelectAll() bool { return bool(C.cageLoginSelectAll()) }
func (vzSurface) Image(region []int) ([]byte, error) {
	var size C.int
	var data unsafe.Pointer
	if len(region) == 0 {
		data = C.cageLoginImage(&size)
	} else if len(region) == 4 {
		data = C.cageLoginImageRegion(C.int(region[0]), C.int(region[1]), C.int(region[2]), C.int(region[3]), &size)
	}
	if data == nil {
		return nil, errors.New("no login frame")
	}
	defer C.free(data)
	return C.GoBytes(data, size), nil
}

func runVZ(ctx context.Context, machine *apple.VirtualMachine, dir string) error {
	if !ownsLoginDirectory(dir) {
		return errors.New("invalid local macOS login view")
	}
	ref, ok := any(machine).(objectReference)
	if !ok {
		return errors.New("this build has no macOS login view")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveLogin(runCtx, dir, vzSurface{}); C.cageLoginStop() }()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		running := false
		for {
			select {
			case <-runCtx.Done():
				C.cageLoginStop()
				return
			case <-ticker.C:
				state := machine.State()
				if state == apple.VirtualMachineStateRunning {
					running = true
				}
				if running && (state == apple.VirtualMachineStateStopped || state == apple.VirtualMachineStateError) {
					C.cageLoginStop()
					return
				}
			}
		}
	}()
	C.cageLoginLoop(ref.CageViewObject())
	runtime.KeepAlive(machine)
	cancel()
	return <-done
}
