package vm

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/sylvinus/agent-vm/internal/paths"
	"golang.org/x/sys/windows"
)

// ProcessCommandLineInformation (Windows 8.1+) returns the target process's
// command line as an NTUnicodeString using only PROCESS_QUERY_LIMITED_INFORMATION.
const processCommandLineInformation int32 = 60

func isWindowsQEMU(pid int, dir, name string) bool {
	if command, ok := windowsProcessCommandLine(pid); ok {
		return windowsQEMUCommandMatches(command, dir, name)
	}
	command, ok := windowsProcessCommandLineCIM(pid)
	if !ok {
		return false
	}
	return windowsQEMUCommandMatches(command, dir, name)
}

func windowsProcessCommandLine(pid int) (string, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(handle)

	size := uint32(4096)
	for range 6 {
		buf := make([]byte, size)
		var retLen uint32
		err := windows.NtQueryInformationProcess(handle, processCommandLineInformation, unsafe.Pointer(&buf[0]), size, &retLen)
		if err == windows.STATUS_INFO_LENGTH_MISMATCH || err == windows.STATUS_BUFFER_TOO_SMALL || err == windows.STATUS_BUFFER_OVERFLOW {
			if retLen > size {
				size = retLen
			} else {
				size *= 2
			}
			if size > 1<<20 {
				return "", false
			}
			continue
		}
		if err != nil {
			return "", false
		}
		if uint32(len(buf)) < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) {
			return "", false
		}
		u := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
		if u.Length == 0 || u.Buffer == nil {
			return "", false
		}
		cmd := u.String()
		runtime.KeepAlive(buf)
		return cmd, cmd != ""
	}
	return "", false
}

func windowsProcessCommandLineCIM(pid int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	cmd := exec.CommandContext(ctx, filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-Command",
		`[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); (Get-CimInstance Win32_Process -Filter 'ProcessId=`+strconv.Itoa(pid)+`').CommandLine | ConvertTo-Json -Compress`)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	var command string
	if json.Unmarshal(out, &command) != nil || command == "" {
		return "", false
	}
	return command, true
}

func windowsQEMUCommandMatches(command, dir, name string) bool {
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || len(args) == 0 || !strings.EqualFold(filepath.Base(args[0]), "qemu-system-x86_64.exe") {
		return false
	}
	gotName, gotPID := false, false
	for i := 1; i+1 < len(args); i++ {
		switch args[i] {
		case "-name":
			gotName = args[i+1] == "lima-"+name
			i++
		case "-pidfile":
			gotPID = pidFileMatchesResolved(args[i+1], dir, paths.Resolve)
			i++
		}
	}
	return gotName && gotPID
}

func qmpSocketCandidatesWindows(instDir string) []string {
	candidates := []string{filepath.Join(instDir, filenames.QMPSock)}
	if short := windowsShortPath(instDir); short != "" {
		candidates = append(candidates, filepath.Join(short, filenames.QMPSock))
	}
	if resolved, err := paths.Resolve(instDir); err == nil && resolved != "" {
		candidates = append(candidates, filepath.Join(resolved, filenames.QMPSock))
		if short := windowsShortPath(resolved); short != "" {
			candidates = append(candidates, filepath.Join(short, filenames.QMPSock))
		}
	}
	return candidates
}

func windowsShortPath(path string) string {
	long, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(long, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || int(n) >= len(buffer) {
		return ""
	}
	return windows.UTF16ToString(buffer[:n])
}
