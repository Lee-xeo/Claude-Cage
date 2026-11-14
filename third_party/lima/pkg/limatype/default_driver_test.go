package limatype

import "testing"

func TestDefaultDriverMatchesHostArchitecture(t *testing.T) {
	for _, tc := range []struct {
		platform, arch string
		want           VMType
	}{
		{"darwin", "amd64", QEMU},
		{"darwin", "arm64", VZ},
		{"linux", "amd64", QEMU},
		{"windows", "amd64", QEMU},
	} {
		if got := defaultDriverFor(tc.platform, tc.arch); got != tc.want {
			t.Fatalf("%s/%s: driver %s, want %s", tc.platform, tc.arch, got, tc.want)
		}
	}
}
