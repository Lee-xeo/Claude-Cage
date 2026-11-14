//go:build !darwin

package limaembed

import "embed"

// Exclude the Darwin directory even when cross-compiling in a Mac checkout.
//
//go:embed all:assets/[^D]*
var assets embed.FS
