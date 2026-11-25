package vm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/store"
	"github.com/sylvinus/agent-vm/internal/host"
	"golang.org/x/sys/windows"
)

func stopInstance(ctx context.Context, inst *limatype.Instance) error {
	requested, err := host.RequestInstanceStop(inst.Dir)
	if err != nil {
		return err
	}
	if !requested {
		// The hostagent may have exited right before RequestInstanceStop opened
		// its stop event, leaving the VM already stopped or QEMU orphaned.
		if current, inspectErr := store.Inspect(ctx, inst.Name); inspectErr == nil {
			switch evaluateWindowsStopPoll(current.Status, current.HostAgentPID, current.DriverPID, fromLima(current).Orphaned) {
			case stopPollStopped:
				return nil
			case stopPollStopOrphan:
				return stopOrphanedInstance(ctx, current)
			}
		}
		return fmt.Errorf("Cage 环境的停止通知不可用；保留环境，请查看 hostagent 日志")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute+10*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 Cage 环境保存并停止超时: %w", ctx.Err())
		case <-ticker.C:
			current, err := store.Inspect(ctx, inst.Name)
			if err != nil {
				return err
			}
			switch evaluateWindowsStopPoll(current.Status, current.HostAgentPID, current.DriverPID, fromLima(current).Orphaned) {
			case stopPollStopped:
				return nil
			case stopPollStopOrphan:
				return stopOrphanedInstance(ctx, current)
			case stopPollBrokenFail:
				return fmt.Errorf("Cage 宿主代理已退出但环境处于异常状态（%s），无法确认安全停止；请查看 hostagent 日志", current.Status)
			}
		}
	}
}

func stopOrphanedInstance(ctx context.Context, inst *limatype.Instance) error {
	if inst.VMType != limatype.QEMU || inst.DriverPID <= 0 {
		return fmt.Errorf("unsupported orphaned VM driver")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(inst.DriverPID))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	// An exited QEMU kept open by another handle no longer reports its image
	// name; it has already stopped.
	if state, err := windows.WaitForSingleObject(handle, 0); err == nil && state == windows.WAIT_OBJECT_0 {
		cleanupInstanceTmpFiles(inst.Dir)
		return nil
	}
	image := make([]uint16, 32768)
	size := uint32(len(image))
	if err := windows.QueryFullProcessImageName(handle, 0, &image[0], &size); err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Base(windows.UTF16ToString(image[:size])), "qemu-system-x86_64.exe") || !isQEMU(inst.DriverPID, inst.Dir, inst.Name) {
		return fmt.Errorf("orphaned VM process identity changed; no process was stopped")
	}
	waitExit := func(d time.Duration) (bool, error) {
		ms := uint32(0)
		if d > 0 {
			ms = uint32(d.Milliseconds())
			if ms == 0 {
				ms = 1
			}
		}
		state, err := windows.WaitForSingleObject(handle, ms)
		if err != nil {
			return false, err
		}
		return state == windows.WAIT_OBJECT_0, nil
	}
	forceStop := func() error {
		if err := windows.TerminateProcess(handle, 1); err != nil {
			if exited, waitErr := waitExit(0); waitErr == nil && exited {
				return nil
			}
			return err
		}
		state, err := windows.WaitForSingleObject(handle, 5000)
		if err != nil {
			return err
		}
		if state != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("orphaned QEMU did not stop")
		}
		return nil
	}
	return stopOrphanedWithHooks(ctx, inst.Dir, qmpSocketCandidatesWindows(inst.Dir), waitExit, forceStop)
}
