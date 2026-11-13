package guestos

import "testing"

func TestValidMacMaintenancePurpose(t *testing.T) {
	for _, purpose := range []string{"projects", "screen", "input", "sharing"} {
		if !ValidMacMaintenancePurpose(purpose) {
			t.Errorf("%q should be a maintenance purpose", purpose)
		}
	}
	for _, purpose := range []string{"", "desktop", "ssh", "screensharing"} {
		if ValidMacMaintenancePurpose(purpose) {
			t.Errorf("%q should be rejected", purpose)
		}
	}
}
