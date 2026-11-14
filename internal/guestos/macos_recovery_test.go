package guestos

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestIntelMacOSRecoveryKeepsQEMUBackendBeforeBinding(t *testing.T) {
	profile := t.TempDir()
	machine := filepath.Join(profile, "state", "lima", "mac")
	if err := os.MkdirAll(machine, 0700); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	identity := IntelMacOSIdentity{Version: 1, Backend: MacOSQEMU, UUID: uuid.NewString(), Installation: id, BootSHA256: strings.Repeat("a", 64)}
	if err := WriteIntelMacOSIdentity(machine, identity); err != nil {
		t.Fatal(err)
	}
	config := "os: Darwin\narch: x86_64\nvmType: qemu\nvmOpts:\n  qemu:\n    macOS:\n      uuid: " + identity.UUID + "\n"
	if err := os.WriteFile(filepath.Join(machine, "lima.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "disk"), []byte("partial-system"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveInstallation(profile, Installation{Version: 1, System: MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := CreateMacOSInstallRecovery(profile, "cageuser", id); err != nil {
		t.Fatal(err)
	}
	recovery, err := LoadMacOSProfileOrRecovery(profile)
	if err != nil || recovery == nil || recovery.Backend != MacOSQEMU || recovery.UUID != identity.UUID || recovery.SetupID != id.String() {
		t.Fatalf("pending Intel recovery lost the display backend: %+v %v", recovery, err)
	}
	if bound, err := LoadMacOSProfile(profile); err != nil || bound != nil {
		t.Fatalf("recovery view prematurely bound the guest: %+v %v", bound, err)
	}
}

func TestMacOSPendingInstallCanEnterRecoveryAfterFailure(t *testing.T) {
	profile := t.TempDir()
	machine := filepath.Join(profile, "state", "lima", "mac")
	if err := os.MkdirAll(machine, 0700); err != nil {
		t.Fatal(err)
	}
	identifier := []byte("partial-macos-27-machine")
	if err := os.WriteFile(filepath.Join(machine, "vz-identifier"), identifier, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "disk"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := SaveInstallation(profile, Installation{Version: 1, System: MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	canRecover, err := CanRecoverMacOSInstall(profile, id)
	if err != nil || !canRecover {
		t.Fatalf("pending guest could not enter recovery: %v %v", canRecover, err)
	}
	if err := CreateMacOSInstallRecovery(profile, "cageuser", id); err != nil {
		t.Fatal(err)
	}
	recovery, err := LoadMacOSInstallRecovery(profile)
	if err != nil || recovery == nil || recovery.InstallationID != id || recovery.User != "cageuser" {
		t.Fatalf("recovery is not bound to the pending guest: %+v %v", recovery, err)
	}
	machineID, err := macMachineID(profile)
	if err != nil || recovery.MachineID != machineID {
		t.Fatalf("recovery is not bound to the VZ identity: %+v %v", recovery, err)
	}
	if _, err := os.Stat(machine); err != nil {
		t.Fatalf("partial guest disk was lost: %v", err)
	}
	pending, err := LoadInstallation(profile)
	if err != nil || pending == nil || pending.ID != id {
		t.Fatalf("pending installation was lost: %+v %v", pending, err)
	}
}

func TestMacOSInstallRecoveryRetainsPendingAfterCancellation(t *testing.T) {
	profile := t.TempDir()
	machine := filepath.Join(profile, "state", "lima", "mac")
	if err := os.MkdirAll(machine, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "vz-identifier"), []byte("macos-27-machine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "disk"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := SaveInstallation(profile, Installation{Version: 1, System: MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := CreateMacOSInstallRecovery(profile, "cageuser", id); err != nil {
		t.Fatal(err)
	}
	cancelled := errors.New("用户取消 Apple Installer 授权")
	if err := SetMacOSInstallRecoveryPhase(profile, id, MacOSInstallAwaitingGUIAuthorization, cancelled); err != nil {
		t.Fatal(err)
	}
	recovery, err := LoadMacOSInstallRecovery(profile)
	if err != nil || recovery == nil || recovery.LastError != cancelled.Error() {
		t.Fatalf("recovery was not retained: %+v %v", recovery, err)
	}
	pending, err := LoadInstallation(profile)
	if err != nil || pending == nil || pending.ID != id {
		t.Fatalf("pending install was lost: %+v %v", pending, err)
	}
	if _, err := os.Stat(machine); err != nil {
		t.Fatalf("guest disk was lost: %v", err)
	}
}

func TestMacOSInstallRecoveryRejectsChangedMachine(t *testing.T) {
	profile := t.TempDir()
	machine := filepath.Join(profile, "state", "lima", "mac")
	if err := os.MkdirAll(machine, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "vz-identifier"), []byte("original-machine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "disk"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := SaveInstallation(profile, Installation{Version: 1, System: MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := CreateMacOSInstallRecovery(profile, "cageuser", id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "vz-identifier"), []byte("different-machine"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMacOSInstallRecovery(profile); err == nil {
		t.Fatal("accepted a recovery session for a different guest")
	}
}

func TestBindNewMacOSProfileRequiresPreparedRecovery(t *testing.T) {
	profile := t.TempDir()
	machine := filepath.Join(profile, "state", "lima", "mac")
	if err := os.MkdirAll(machine, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "vz-identifier"), []byte("bind-gated-machine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "disk"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := ssh.MarshalAuthorizedKey(key)
	id := uuid.New()
	if err := SaveInstallation(profile, Installation{Version: 1, System: MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := CreateMacOSInstallRecovery(profile, "cageuser", id); err != nil {
		t.Fatal(err)
	}
	if err := BindNewMacOSProfile(profile, "cageuser", hostKey, id); err == nil {
		t.Fatal("bound a guest before component and service preparation")
	}
	if err := SetMacOSInstallRecoveryPhase(profile, id, MacOSInstallServicesPrepared, nil); err == nil {
		t.Fatal("skipped component and account verification")
	}
	for _, phase := range []string{MacOSInstallComponentsVerified, MacOSInstallAccountVerified} {
		if err := SetMacOSInstallRecoveryPhase(profile, id, phase, nil); err != nil {
			t.Fatal(err)
		}
		if err := BindNewMacOSProfile(profile, "cageuser", hostKey, id); err == nil {
			t.Fatalf("bound after only phase %q", phase)
		}
	}
	if err := BindNewMacOSProfile(profile, "differentuser", hostKey, id); err == nil {
		t.Fatal("bound a guest to an account other than its recovery identity")
	}
	if err := SetMacOSInstallRecoveryPhase(profile, id, MacOSInstallServicesPrepared, nil); err != nil {
		t.Fatal(err)
	}
	if err := BindNewMacOSProfile(profile, "cageuser", hostKey, id); err != nil {
		t.Fatal(err)
	}
}
