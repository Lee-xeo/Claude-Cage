package guestos

import (
	"fmt"
	"io"
	"testing"
)

func TestWindowsInspectionReportsDebianWorkbench(t *testing.T) {
	pass := func(io.Writer) bool { return true }
	fail := func(w io.Writer) bool { fmt.Fprintln(w, "请启用 Windows Hypervisor Platform"); return false }
	unused := func(io.Writer) bool { t.Fatal("host preflight must not run on an unsupported host"); return false }
	for _, build := range []uint32{19045, 22631, 26200} {
		if r := windowsInspection("amd64", build, pass); r.Guest != Debian13 || r.WorkbenchSupport != "supported" || !r.HostCheckPassed {
			t.Fatalf("build %d ready: %+v", build, r)
		}
	}
	if r := windowsInspection("amd64", 19045, fail); r.Guest != Debian13 || r.WorkbenchSupport != "supported" || r.HostCheckPassed || r.Detail != "请启用 Windows Hypervisor Platform" {
		t.Fatalf("missing hypervisor: %+v", r)
	}
	if r := windowsInspection("amd64", 19044, unused); r.Guest != Debian13 || r.WorkbenchSupport != "unsupported-host" || r.HostCheckPassed {
		t.Fatalf("Windows 10 21H2: %+v", r)
	}
	if r := windowsInspection("arm64", 26200, unused); r.Guest != Debian13 || r.WorkbenchSupport != "unsupported-host" {
		t.Fatalf("Windows on ARM: %+v", r)
	}
}
