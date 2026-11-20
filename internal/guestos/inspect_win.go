package guestos

import (
	"bytes"
	"io"
	"strings"
)

// Windows 10 22H2 is the oldest release whose Hypervisor Platform runs the
// bundled QEMU. Windows 10 and 11 have the same Debian workbench conditions.
const windowsMinimumBuild = 19045

// windowsInspection reports the Debian workbench prerequisites of a Windows
// host. check runs the host preflight and writes its reason when it fails.
// It has no build constraint so the decision is testable on every platform.
func windowsInspection(goarch string, build uint32, check func(io.Writer) bool) Inspection {
	r := Inspection{Guest: Debian13, WorkbenchSupport: "supported"}
	if goarch != "amd64" {
		r.WorkbenchSupport = "unsupported-host"
		r.Detail = "Windows 发行包面向 x64 处理器，暂不支持 Windows on ARM。"
		return r
	}
	if build < windowsMinimumBuild {
		r.WorkbenchSupport = "unsupported-host"
		r.Detail = "Debian 工作台需要 64 位 Windows 10 22H2（19045）或 Windows 11。"
		return r
	}
	var details bytes.Buffer
	r.HostCheckPassed = check(&details)
	r.Detail = strings.TrimSpace(details.String())
	if r.HostCheckPassed {
		r.Detail = "已检测到 Debian 工作台所需的 Windows Hypervisor Platform 与随附 QEMU。"
	} else if r.Detail == "" {
		r.Detail = "Windows 主机检查未通过，请运行 cage agent doctor 查看原因。"
	}
	return r
}
