package hostimage

import (
	"net/url"
	"strings"
	"testing"
)

func TestRecoveryURLsRetainSignedCDNTransport(t *testing.T) {
	for _, address := range []string{"https://osrecovery.apple.com/", "http://oscdn.apple.com/content/a", "https://swcdn.apple.com/content/a"} {
		u, _ := url.Parse(address)
		if err := validateRecoveryURL(u); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"http://osrecovery.apple.com/", "https://oscdn.apple.com.evil.test/a", "http://user@oscdn.apple.com/a", "http://oscdn.apple.com:80/a", "file:///tmp/a"} {
		u, _ := url.Parse(address)
		if validateRecoveryURL(u) == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}

func TestIntelMacCompatiblePatchWithoutMajorUpgrade(t *testing.T) {
	for _, tc := range []struct {
		host, guest string
		allow       bool
	}{
		{"15.8.2", "15.8.2", true},
		{"15.8.2", "15.8.3", true},
		{"14.8.2", "14.8.3", false},
		{"15.4", "15.4.1", true},
		{"26.1", "26.2", true},
		{"15.8.3", "15.8.2", false},
		{"15.8.2", "26.1", false},
		{"13.7.1", "13.7.2", false},
		{"invalid", "15.8.3", false},
	} {
		t.Run(tc.host+"/"+tc.guest, func(t *testing.T) {
			if got := intelMacVersionCompatible(tc.host, tc.guest); got != tc.allow {
				t.Fatalf("compatible = %v", got)
			}
		})
	}
}

func TestIntelMacPlanRetainsPhysicalHostIdentity(t *testing.T) {
	p := Plan{Host: Identity{System: "macos", Arch: "amd64", Version: "15.8.2", Build: "24H232"}, Version: "15.8.3", Build: "24H241", Format: "pkgdmg", URL: "https://swcdn.apple.com/InstallAssistant.pkg", Bytes: 123,
		IntegrityURL: "https://swcdn.apple.com/InstallAssistant.pkg.integrityDataV1", IntegritySHA256: strings.Repeat("a", 64)}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Host.Arch = "arm64"
	if err := p.Validate(); err == nil {
		t.Fatal("Intel package accepted for Apple silicon")
	}
	p.Host.Arch = "amd64"
	p.Version = "26.1"
	if err := p.Validate(); err == nil {
		t.Fatal("major upgrade accepted without a new selection")
	}
}
