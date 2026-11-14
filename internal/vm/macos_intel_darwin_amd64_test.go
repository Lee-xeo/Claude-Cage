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
	} {
		if got := intelMacOSInstallStage([]byte(test.serial)); got != test.want {
			t.Errorf("stage=%q; want %q", got, test.want)
		}
	}
}
