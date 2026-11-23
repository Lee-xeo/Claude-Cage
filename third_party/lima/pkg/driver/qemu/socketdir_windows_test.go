//go:build windows

// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package qemu

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSocketDirResolvesChineseJunctionToASCIIStorage(t *testing.T) {
	base := t.TempDir()
	storage := filepath.Join(base, "storage")
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "测试别名")
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, storage)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mklink /J failed: %v (%s)", err, string(out))
	}
	t.Cleanup(func() { _ = os.Remove(junction) })

	got := socketDir(junction)
	if !isASCII(got) {
		t.Fatalf("socketDir(%q) = %q, want ASCII path", junction, got)
	}
	wantInfo, err := os.Stat(storage)
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat(%q) failed: %v", got, err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("socketDir(%q) = %q does not refer to %q", junction, got, storage)
	}
}
