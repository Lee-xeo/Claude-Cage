package guestos

import "context"

// A Windows host has no same-system guest: Windows guests are no longer supported.
func Inspect(_ context.Context) Inspection {
	return Inspection{WorkbenchSupport: "unsupported-host", Detail: "Cage 已不再支持 Windows 客机，Windows 主机仅提供 Debian 环境。"}
}
