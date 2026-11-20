package guestos

// macInspection reports the Debian workbench prerequisites of a Mac host.
// It has no build constraint so the decision is testable on every platform.
func macInspection(goarch string, hypervisor bool) Inspection {
	r := Inspection{Guest: Debian13, WorkbenchSupport: "supported"}
	if goarch != "arm64" {
		r.WorkbenchSupport = "unsupported-host"
		r.Detail = "Mac 发行包面向 Apple Silicon，Intel Mac 暂不支持。"
		return r
	}
	r.HostCheckPassed = hypervisor
	if hypervisor {
		r.Detail = "已检测到 Debian 工作台所需的 macOS 系统虚拟化。"
	} else {
		r.Detail = "未检测到 macOS 系统虚拟化，无法运行 Debian 工作台。"
	}
	return r
}
