package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
)

var (
	orphanGracefulWait  = 15 * time.Second
	qmpPowerdownTimeout = 3 * time.Second
)

type stopPollAction int

const (
	stopPollWait stopPollAction = iota
	stopPollStopped
	stopPollStopOrphan
	stopPollBrokenFail
)

// evaluateWindowsStopPoll decides the next action while waiting for a graceful
// Windows hostagent stop. If the hostagent exits mid-stop leaving a verified
// orphaned QEMU behind, transition directly to orphan cleanup instead of
// waiting out the full shutdown timeout.
func evaluateWindowsStopPoll(status limatype.Status, hostAgentPID, driverPID int, orphaned bool) stopPollAction {
	switch {
	case status == limatype.StatusStopped:
		return stopPollStopped
	case status == limatype.StatusBroken && orphaned:
		return stopPollStopOrphan
	case status == limatype.StatusBroken && hostAgentPID == 0:
		return stopPollBrokenFail
	default:
		return stopPollWait
	}
}

// sendQMPPowerdown attempts to send a standard QEMU QMP system_powerdown
// command over the first reachable Unix domain socket in candidates.
func sendQMPPowerdown(ctx context.Context, candidates []string) error {
	var lastErr error
	seen := make(map[string]struct{}, len(candidates))
	for _, sockPath := range candidates {
		if sockPath == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(sockPath))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if err := sendQMPPowerdownOnce(ctx, sockPath); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		return errors.New("no QMP socket candidates")
	}
	return lastErr
}

func sendQMPPowerdownOnce(ctx context.Context, sockPath string) error {
	timeout := qmpPowerdownTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 64*1024)

	// Read the QMP greeting banner.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return err
		}
		return errors.New("QMP closed before greeting")
	}
	var greeting map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &greeting); err != nil {
		return fmt.Errorf("invalid QMP greeting: %w", err)
	}
	if _, ok := greeting["QMP"]; !ok {
		return errors.New("unexpected QMP greeting")
	}

	for _, cmd := range []string{"qmp_capabilities", "system_powerdown"} {
		if _, err := fmt.Fprintf(conn, "{\"execute\":%q}\n", cmd); err != nil {
			return err
		}
		if err := readQMPReply(scanner); err != nil {
			return fmt.Errorf("QMP %s failed: %w", cmd, err)
		}
	}
	return nil
}

func readQMPReply(scanner *bufio.Scanner) error {
	for range 32 {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return errors.New("QMP connection closed before reply")
		}
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return err
		}
		if _, isEvent := msg["event"]; isEvent {
			continue
		}
		if errObj, hasErr := msg["error"]; hasErr {
			return fmt.Errorf("QMP error: %v", errObj)
		}
		if _, hasReturn := msg["return"]; hasReturn {
			return nil
		}
	}
	return errors.New("too many QMP events without command reply")
}

// cleanupInstanceTmpFiles removes stale PID and socket files left behind when
// an orphaned driver exits without its hostagent, matching Lima's StopForcibly.
func cleanupInstanceTmpFiles(instDir string) {
	if instDir == "" {
		return
	}
	entries, err := os.ReadDir(instDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		for _, suffix := range filenames.TmpFileSuffixes {
			if strings.HasSuffix(name, suffix) {
				_ = os.Remove(filepath.Join(instDir, name))
				break
			}
		}
	}
}

// stopOrphanedWithHooks attempts a graceful QMP system_powerdown first and
// waits within a bounded window for the orphaned QEMU process to exit before
// falling back to forceStop. Always cleans up stale tmp files once stopped.
func stopOrphanedWithHooks(
	ctx context.Context,
	instDir string,
	qmpCandidates []string,
	waitExit func(time.Duration) (bool, error),
	forceStop func() error,
) error {
	if exited, err := waitExit(0); err == nil && exited {
		cleanupInstanceTmpFiles(instDir)
		return nil
	}
	if err := sendQMPPowerdown(ctx, qmpCandidates); err == nil {
		deadline := time.Now().Add(orphanGracefulWait)
		for time.Now().Before(deadline) {
			if ctx.Err() != nil {
				break
			}
			step := time.Until(deadline)
			if step > 250*time.Millisecond {
				step = 250 * time.Millisecond
			}
			exited, waitErr := waitExit(step)
			if waitErr == nil && exited {
				cleanupInstanceTmpFiles(instDir)
				return nil
			}
		}
	}
	if exited, err := waitExit(0); err == nil && exited {
		cleanupInstanceTmpFiles(instDir)
		return nil
	}
	if err := forceStop(); err != nil {
		return err
	}
	cleanupInstanceTmpFiles(instDir)
	return nil
}

// pidFileMatchesResolved checks whether a QEMU -pidfile argument names dir's
// qemu.pid, handling mixed slashes, case differences, 8.3 aliases, and
// junctions/symlinks via resolve.
func pidFileMatchesResolved(arg, dir string, resolve func(string) (string, error)) bool {
	norm := func(p string) string {
		slash := strings.ReplaceAll(p, `\`, "/")
		unc := strings.HasPrefix(slash, "//")
		cleaned := filepath.ToSlash(filepath.Clean(slash))
		if unc && !strings.HasPrefix(cleaned, "//") {
			cleaned = "/" + cleaned
		}
		return strings.ToLower(strings.TrimRight(strings.ReplaceAll(cleaned, "/", `\`), `\`))
	}
	expected := strings.TrimRight(dir, `\/`) + `\qemu.pid`
	if norm(arg) == norm(expected) {
		return true
	}
	argSlash := strings.ReplaceAll(arg, `\`, "/")
	if !strings.EqualFold(filepath.Base(argSlash), "qemu.pid") {
		return false
	}
	if resolve == nil {
		return false
	}
	if a, err := resolve(arg); err == nil {
		if b, err := resolve(expected); err == nil && norm(a) == norm(b) {
			return true
		}
	}
	// Resolve parent directories in case qemu.pid itself was already unlinked
	// or exists under a junction whose spelling differs from dir.
	argDir := parentPathAnySep(arg)
	if argDir == "" {
		return false
	}
	if aDir, err := resolve(argDir); err == nil {
		if bDir, err := resolve(dir); err == nil && norm(aDir) == norm(bDir) {
			return true
		}
	}
	return false
}

func parentPathAnySep(p string) string {
	trimmed := strings.TrimRight(p, `\/`)
	idx := strings.LastIndexAny(trimmed, `\/`)
	if idx <= 0 {
		return ""
	}
	return trimmed[:idx]
}
