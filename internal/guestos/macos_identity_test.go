package guestos

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func intelBindingFixture(t *testing.T) (string, string) {
	t.Helper()
	profile := t.TempDir()
	dir := filepath.Join(profile, "state", "lima", "mac")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	identity := IntelMacOSIdentity{Version: 1, Backend: MacOSQEMU, UUID: uuid.NewString(), Installation: uuid.New(), BootSHA256: strings.Repeat("a", 64)}
	if err := WriteIntelMacOSIdentity(dir, identity); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("os: Darwin\narch: x86_64\nvmType: qemu\nvmOpts:\n  qemu:\n    macOS:\n      uuid: %s\n", identity.UUID)
	if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindMacOSProfile(profile, "developer", ssh.MarshalAuthorizedKey(key)); err != nil {
		t.Fatal(err)
	}
	return profile, dir
}

func TestIntelMacBindingRejectsBackendAndIdentityChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(string, string) error
	}{
		{"firmware-uuid", func(_, dir string) error {
			p := filepath.Join(dir, "lima.yaml")
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			identity, err := ReadIntelMacOSIdentity(dir)
			if err != nil {
				return err
			}
			return os.WriteFile(p, []byte(strings.ReplaceAll(string(data), identity.UUID, uuid.NewString())), 0600)
		}},
		{"writable-identity", func(_, dir string) error { return os.Chmod(filepath.Join(dir, IntelMacOSIdentityFile), 0666) }},
		{"conflicting-vz", func(_, dir string) error {
			return os.WriteFile(filepath.Join(dir, "vz-identifier"), []byte("other backend"), 0600)
		}},
		{"legacy-binding", func(profile, _ string) error {
			p := filepath.Join(profile, "macos-guest.json")
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(p, []byte(strings.Replace(string(data), `"version":2`, `"version":1`, 1)), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, dir := intelBindingFixture(t)
			bound, err := LoadMacOSProfile(profile)
			if err != nil || bound == nil || bound.Backend != MacOSQEMU {
				t.Fatal(bound, err)
			}
			if err := tc.mutate(profile, dir); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadMacOSProfile(profile); err == nil {
				t.Fatal("changed virtual machine identity accepted")
			}
		})
	}
}
