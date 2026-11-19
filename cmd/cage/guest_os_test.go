package main

import (
	"bytes"
	"testing"
)

func TestGuestOSInspectionAcceptsOnlyJSONFlag(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--json", "extra"},
		{"--json", "--vm-id", "ad2d0746-c50c-4dbd-a5f3-495ccfa4cbc2"},
	} {
		var out bytes.Buffer
		if err := writeGuestOSInspection(t.Context(), args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("unexpected guest inspection for %v: %v, %q", args, err, out.String())
		}
	}
}
