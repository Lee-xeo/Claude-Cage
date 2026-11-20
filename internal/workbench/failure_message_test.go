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
