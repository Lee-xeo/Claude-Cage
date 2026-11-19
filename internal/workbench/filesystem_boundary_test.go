package workbench

import "testing"

func TestFilesystemBoundaryRequiresSharedRoundTripAndPrivateRefusal(t *testing.T) {
	for _, test := range []struct {
		name, private, data  string
		layout, shared, want bool
	}{
		{"round trip", "unreachable", "nonce:guest", true, true, true},
		{"not writable", "unreachable", "nonce", true, true, false},
		{"unexpected write", "unreachable", "changed", true, true, false},
		{"observation absent", "unreachable", "nonce:guest", true, false, false},
		{"private readable", "readable", "nonce:guest", true, true, false},
		{"private unknown", "unknown", "nonce:guest", true, true, false},
		{"unexpected mount", "unreachable", "nonce:guest", false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary := guestObservation{PrivateFile: test.private, SharedFile: test.shared}
			if got := filesystemBoundaryVerified(test.layout, boundary, test.data, "nonce"); got != test.want {
				t.Fatalf("boundary result=%t want=%t", got, test.want)
			}
		})
	}
}
