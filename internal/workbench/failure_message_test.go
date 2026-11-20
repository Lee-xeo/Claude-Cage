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

func TestExecutionFailureNamesAPTResolverVariants(t *testing.T) {
	for _, line := range []string{
		"E: Failed to fetch http://deb.debian.org/debian/dists/trixie/InRelease  Something wicked happened resolving 'deb.debian.org:http' (-5 - No address associated with hostname)",
		"W: Unable to fetch: No address associated with hostname",
	} {
		message := executionFailure("准备 Debian 13 镜像", errors.New("exit status 1"), line).Error()
		if !strings.Contains(message, "域名解析失败") {
			t.Fatalf("%q -> %q", line, message)
		}
	}
}

func TestExecutionFailureNamesUnconfirmedStop(t *testing.T) {
	output := "Stopping VM 'claude-cage-downloads-1234'...\nError: VM 'claude-cage-downloads-1234' is still running, or Lima cannot say. See 'claude-cage list'.\n"
	message := executionFailure("停止环境", errors.New("exit status 1"), output).Error()
	if !strings.Contains(message, "无法确认环境已停止") || strings.Contains(message, "可识别") {
		t.Fatalf("%q", message)
	}
}

func TestExecutionFailureNamesMissingQEMU(t *testing.T) {
	for _, line := range []string{
		`level=fatal msg="exec: \"qemu-img\": executable file not found in %PATH%"`,
		"Error: Lima needs 'qemu-system-x86_64' on PATH (not found).",
	} {
		message := executionFailure("准备 Debian 13 镜像", errors.New("exit status 1"), line).Error()
		if !strings.Contains(message, "QEMU 组件") || !strings.Contains(message, "PATHEXT") {
			t.Fatalf("%q -> %q", line, message)
		}
	}
}
