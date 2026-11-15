package vm

import "testing"

func TestIntelMacOSInstallStageIgnoresErrorsBeforeTheCurrentBoot(t *testing.T) {
	const boot = "======== End of efiboot serial output. ========"
	for _, test := range []struct{ serial, want string }{
		{"\nCAGE_INTEL_INSTALL_ERROR\n", "ERROR"},
		{"\nCAGE_INTEL_INSTALL_ERROR\n" + boot, ""},
		{"\nCAGE_INTEL_INSTALL_ERROR\n" + boot + "\nCAGE_INTEL_INSTALL_START\n", "START"},
		{"\nCAGE_INTEL_INSTALL_START\n" + boot + "\nCAGE_INTEL_INSTALL_ERROR\n", "ERROR"},
		{boot + "\nCAGE_INTEL_INSTALL_PREPARE\r\n", "PREPARE"},
		{boot + "\nCAGE_INTEL_INSTALL_START\n\nCAGE_INTEL_INSTALL_PROGRESS prepare 40\n", "START"},
		{boot + "\nCAGE_INTEL_INSTALL_PROGRESS copy 90\n\nCAGE_INTEL_INSTALL_ERROR\n", "ERROR"},
	} {
		if got := intelMacOSInstallStage([]byte(test.serial)); got != test.want {
			t.Errorf("stage=%q; want %q", got, test.want)
		}
	}
}

func TestIntelMacOSInstallProgressUsesTheLatestCompleteLine(t *testing.T) {
	const boot = "======== End of efiboot serial output. ========"
	for _, test := range []struct {
		serial, phase string
		percent       int
		ok            bool
	}{
		{"\nCAGE_INTEL_INSTALL_PROGRESS copy 10\n\nCAGE_INTEL_INSTALL_PROGRESS copy 55\n", "copy", 55, true},
		{"\nCAGE_INTEL_INSTALL_PROGRESS prepare 30\n\nCAGE_INTEL_INSTALL_PROGRESS prepare 3", "", 0, false},
		{"\nCAGE_INTEL_INSTALL_PROGRESS copy 70\n" + boot, "", 0, false},
		{"\nCAGE_INTEL_INSTALL_PROGRESS shell 70\n", "", 0, false},
		{"\nCAGE_INTEL_INSTALL_PROGRESS prepare 101\n", "", 0, false},
	} {
		phase, percent, ok := intelMacOSInstallProgress([]byte(test.serial))
		if phase != test.phase || percent != test.percent || ok != test.ok {
			t.Errorf("%q: got %q %d %t", test.serial, phase, percent, ok)
		}
	}
	if got := intelMacOSBootCount([]byte("#[EB|LOG:HANDOFF TO XNU] _\nx\n#[EB|LOG:HANDOFF TO XNU] _\n")); got != 2 {
		t.Errorf("boot count = %d", got)
	}
}
