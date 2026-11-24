package workbench

import (
	"errors"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
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

func TestExecutionFailureNamesUnmountablePath(t *testing.T) {
	output := "Error: project path contains whitespace, which Lima cannot mount:\n  C:/Users/Jae Young/.claude-cage-downloads\nRename the directory, then retry.\n"
	message := executionFailure("启动环境", errors.New("exit status 1"), output).Error()
	if !strings.Contains(message, "含有空格") || strings.Contains(message, "可识别") {
		t.Fatalf("%q", message)
	}
}

func TestExecutionFailureNamesInterruptedTLSHandshake(t *testing.T) {
	for _, line := range []string{
		"curl: (35) TLS connect error: error:0A000126:SSL routines::unexpected eof while reading",
		"curl: (35) OpenSSL SSL_connect: SSL_ERROR_SYSCALL in connection to downloads.claude.ai:443",
	} {
		output := "curl is already the newest version (8.14.1-2+deb13u7).\n正在验证 Anthropic 安装源…\n" + line + "\n"
		message := executionFailure("安装或更新环境软件", errors.New("exit status 35"), output).Error()
		if !strings.Contains(message, "TLS") || !strings.Contains(message, "TLS 1.2") || strings.Contains(message, "可识别") {
			t.Fatalf("%q -> %q", line, message)
		}
	}
}

func TestExecutionFailureNamesInterruptedPackageInstallation(t *testing.T) {
	output := "Debian mirror unavailable; using the next source: [url]\nE: dpkg was interrupted, you must manually run 'sudo dpkg --configure -a' to correct the problem.\n"
	message := executionFailure("启动环境", errors.New("exit status 100"), output).Error()
	if !strings.Contains(message, "上次中断的软件安装未能自动补完") || strings.Contains(message, "可识别") {
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

func TestExecutionFailureNamesLimaHomeConflict(t *testing.T) {
	rawSamples := []string{
		`Error: could not query Lima: agent-vm's Lima home C:\Users\alice\.claude-cage\state\lima is also your own Lima's home: set AGENT_VM_LIMA_HOME to a folder for agent-vm alone`,
		`agent-vm's Lima home /Users/alice/.claude-cage/state/lima is also your own Lima's home: set AGENT_VM_LIMA_HOME to a folder for agent-vm alone`,
		`agent-vm's Lima home /home/alice/.claude-cage/state/lima is also your own Lima's home: set AGENT_VM_LIMA_HOME to a folder for agent-vm alone`,
		`agent-vm's Lima home /data/.claude-cage/state/lima is also your own Lima's home: set AGENT_VM_LIMA_HOME to a folder for agent-vm alone`,
	}
	for _, raw := range rawSamples {
		for _, output := range []string{raw, diagnostics.Redact(raw)} {
			for _, action := range []string{"停止环境", "启动环境"} {
				message := executionFailure(action, errors.New("exit status 1"), output).Error()
				if !strings.Contains(message, "LIMA_HOME") || !strings.Contains(message, "官方安装包") || strings.Contains(message, "可识别") {
					t.Fatalf("%s %q -> %q", action, output, message)
				}
			}
		}
	}
}

func TestExecutionFailureNamesLimaSocketPath(t *testing.T) {
	for _, line := range []string{
		"Error: instance name `base-building` too long: `C:\\Users\\alice\\private\\ssh.sock.1234` must be less than UNIX_PATH_MAX=108 characters, but is 175",
		"Error: instance name `base-building` too long: `[home path]",
		"Error: instance name \"my-workspace-12345678\" too long: `[home path]`",
		"Error: socket path `/Users/alice/state/ssh.sock` is too long: >= UNIX_PATH_MAX=104",
		"Error: VM name too long: its sockets in /Users/alice/state would need 175 characters, at most 107",
		"Error: " + cage.LimaSocketPathReason,
	} {
		for _, output := range []string{line, diagnostics.Redact(line)} {
			message := executionFailure("准备 Debian 13 镜像", errors.New("exit status 1"), output).Error()
			if !strings.Contains(message, cage.LimaSocketPathReason) || !strings.Contains(message, "更短的本地目录") ||
				strings.Contains(message, "alice") {
				t.Fatalf("%q -> %q", output, message)
			}
		}
	}
	for _, line := range []string{
		"Error: approval request too long",
		"Error: approval request too long: `[home path]`",
	} {
		if msg := executionFailure("审核请求", errors.New("exit status 1"), line).Error(); strings.Contains(msg, cage.LimaSocketPathReason) {
			t.Fatalf("unrelated error reported as a Lima socket failure: %q", msg)
		}
	}
}
