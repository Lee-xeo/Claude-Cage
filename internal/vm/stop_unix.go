//go:build !windows

package vm

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lima-vm/lima/v2/pkg/instance"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
)

func stopInstance(ctx context.Context, inst *limatype.Instance) error {
	return instance.StopGracefully(ctx, inst, false)
}

func stopOrphanedInstance(ctx context.Context, inst *limatype.Instance) error {
	if inst.VMType != limatype.QEMU || inst.DriverPID <= 0 {
		instance.StopForcibly(inst)
		return nil
	}
	waitExit := func(d time.Duration) (bool, error) {
		exited := func() bool {
			err := syscall.Kill(inst.DriverPID, 0)
			return errors.Is(err, syscall.ESRCH)
		}
		if exited() {
			return true, nil
		}
		if d <= 0 {
			return false, nil
		}
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
			if exited() {
				return true, nil
			}
		}
		return exited(), nil
	}
	return stopOrphanedWithHooks(
		ctx,
		inst.Dir,
		[]string{filepath.Join(inst.Dir, filenames.QMPSock)},
		waitExit,
		func() error {
			instance.StopForcibly(inst)
			return nil
		},
	)
}
