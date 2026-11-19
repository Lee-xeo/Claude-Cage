package guestos

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallationKeepsChoiceAndWithdrawsWithoutTouchingVM(t *testing.T) {
	p := t.TempDir()
	first := Installation{Version: 1, System: MacOS, ID: uuid.New()}
	if err := SaveInstallation(p, first); err != nil {
		t.Fatal(err)
	}
	if err := SaveInstallation(p, first); err != nil {
		t.Fatal(err)
	}
	other := first
	other.ID = uuid.New()
	if err := SaveInstallation(p, other); err == nil {
		t.Fatal("overwrote an active installation")
	}
	if err := ClearUnstartedInstallation(p, other.ID); err == nil {
		t.Fatal("stale reset changed selection")
	}
	disk := filepath.Join(p, "state", "lima", "mac")
	if err := os.MkdirAll(disk, 0700); err != nil {
		t.Fatal(err)
	}
	// The installation can no longer continue, so the record goes while
	// whatever was created for it stays.
	if err := ClearUnstartedInstallation(p, first.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadInstallation(p); err != nil || got != nil {
		t.Fatalf("record remains: %v %v", got, err)
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatalf("partial VM was touched: %v", err)
	}
}

func TestWindowsInstallRequiresMediaDigestAndEditorAgreement(t *testing.T) {
	i := Installation{Version: 1, System: Windows11, ID: uuid.New(), Media: filepath.Join(t.TempDir(), "system.iso"), SHA256: strings.Repeat("a", 64)}
	if err := i.Validate(); err == nil {
		t.Fatal("accepted without the editor agreement")
	}
	i.EditorTerms = true
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	i.SHA256 = "bad"
	if err := i.Validate(); err == nil {
		t.Fatal("accepted an incomplete ISO digest")
	}
}

func TestMacInstallAcceptsLocalRestoreImageDigest(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "system.ipsw")
	if err := os.WriteFile(media, []byte("restore image"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("restore image"))
	digest := hex.EncodeToString(sum[:])
	i := Installation{Version: 1, System: MacOS, ID: uuid.New(), Media: media, SHA256: digest}
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]Installation{
		"short digest":         {Version: 1, System: MacOS, ID: i.ID, Media: media, SHA256: "bad"},
		"digest without media": {Version: 1, System: MacOS, ID: i.ID, SHA256: digest},
		"other image format":   {Version: 1, System: MacOS, ID: i.ID, Media: filepath.Join(dir, "system.iso"), SHA256: digest},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if err := (Installation{Version: 1, System: MacOS, ID: i.ID, Media: media, SHA256: "bad"}).Validate(); err == nil || strings.Contains(err.Error(), ".ipsw") {
		t.Fatalf("digest error should not blame the image format: %v", err)
	}
}
