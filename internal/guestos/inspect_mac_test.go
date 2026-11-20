package guestos

import "testing"

func TestMacInspectionReportsDebianWorkbench(t *testing.T) {
	ready := macInspection("arm64", true)
	if ready.Guest != Debian13 || ready.WorkbenchSupport != "supported" || !ready.HostCheckPassed {
		t.Fatalf("Apple Silicon with hypervisor: %+v", ready)
	}
	missing := macInspection("arm64", false)
	if missing.Guest != Debian13 || missing.WorkbenchSupport != "supported" || missing.HostCheckPassed || missing.Detail == "" {
		t.Fatalf("Apple Silicon without hypervisor: %+v", missing)
	}
	intel := macInspection("amd64", true)
	if intel.Guest != Debian13 || intel.WorkbenchSupport != "unsupported-host" || intel.HostCheckPassed {
		t.Fatalf("Intel Mac: %+v", intel)
	}
}
