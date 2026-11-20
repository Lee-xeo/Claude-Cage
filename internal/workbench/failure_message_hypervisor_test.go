package workbench

import (
	"errors"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/host"
)

func TestExecutionFailureNamesWindowsHypervisorPreflight(t *testing.T) {
	for _, line := range []string{host.HypervisorNotReady, host.HypervisorPlatformMissing} {
		output := "校验系统材料 696.8 / 696.8 MiB（100%）\nStarting agent-vm setup...\n" + line + "\n"
		message := executionFailure("准备 Ubuntu 22.04 镜像", errors.New("exit status 1"), output).Error()
		if !strings.Contains(message, "Windows 虚拟化尚未就绪") || !strings.Contains(message, "Windows Hypervisor Platform") || strings.Contains(message, "可识别") {
			t.Fatalf("%q -> %q", line, message)
		}
	}
}
