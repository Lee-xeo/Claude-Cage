package limaembed

import "embed"

// A checkout that built an earlier version may still hold assets/Darwin, the
// guest agent of the removed macOS guest. Leave it out of the binary.
//
//go:embed all:assets/[^D]*
var assets embed.FS
