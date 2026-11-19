package guestos

import "context"

// A Mac host has no same-system guest: macOS guests are no longer supported.
func Inspect(_ context.Context) Inspection {
	return Inspection{WorkbenchSupport: "unsupported-host", Detail: "Cage 已不再支持 macOS 客机，Mac 主机仅提供 Debian 环境。"}
}
