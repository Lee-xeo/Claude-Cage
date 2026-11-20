// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package qemu

import (
	"runtime"
	"testing"
)

func TestSocketDirKeepsASCIIPaths(t *testing.T) {
	for _, dir := range []string{`C:\Users\dev\.claude-cage\state\lima\a`, "/home/dev/.lima/a"} {
		if got := socketDir(dir); got != dir {
			t.Errorf("%q -> %q", dir, got)
		}
	}
	if runtime.GOOS != "windows" {
		if dir := "/home/鲸落/.lima/a"; socketDir(dir) != dir {
			t.Error("only Windows rewrites socket paths")
		}
	}
	if isASCII("C:\\Users\\鲸落") || !isASCII(`C:\Users\x`) {
		t.Error("isASCII")
	}
}
