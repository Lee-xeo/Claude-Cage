package vm

import (
	"encoding/json"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/limayaml"
	"github.com/lima-vm/lima/v2/pkg/yqutil"
)

func TestLimaManualHostnameAndAccounts(t *testing.T) {
	l := newTestLima(t)
	profile := t.TempDir()
	path := filepath.Join(profile, "config.json")
	b, err := json.Marshal(cage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := guestprofile.Create(profile, guestprofile.Options{Hostname: "MY-PC", Username: "alex", BrowserUsername: "web"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(cage.ConfigEnv, path)
	none := []Mount{}
	if err = l.Create(t.Context(), "configured", "template:debian-13", Settings{Mounts: &none}, io.Discard); err != nil {
		t.Fatal(err)
	}
	source, err := l.inspect(t.Context(), "configured")
	if err != nil {
		t.Fatal(err)
	}
	if source.Config.GuestIdentity.Hostname != p.Hostname || *source.Config.User.Name != "alex" || *source.Config.User.Home != "/home/alex" {
		t.Fatal("creation did not apply configured name and account")
	}
	if err = l.Clone(t.Context(), "configured", "copy"); err != nil {
		t.Fatal(err)
	}
	copy, err := l.inspect(t.Context(), "copy")
	if err != nil {
		t.Fatal(err)
	}
	if copy.Config.GuestIdentity.Hostname != p.Hostname || copy.Config.GuestIdentity.UUID == source.Config.GuestIdentity.UUID || copy.Config.GuestIdentity.MACAddress == source.Config.GuestIdentity.MACAddress {
		t.Fatal("clone did not retain chosen hostname with fresh hardware identity")
	}
}

func TestLimaCreationCloneAndEditIdentity(t *testing.T) {
	l := newTestLima(t)
	ctx := t.Context()
	none := []Mount{}
	if err := l.Create(ctx, "base", "template:debian-13", Settings{Mounts: &none}, io.Discard); err != nil {
		t.Fatal(err)
	}
	source, err := l.inspect(ctx, "base")
	if err != nil {
		t.Fatal(err)
	}
	original := source.Config.GuestIdentity
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := l.Clone(ctx, "base", "project"); err != nil {
		t.Fatal(err)
	}
	clone, err := l.inspect(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	copied := clone.Config.GuestIdentity
	if err := copied.Validate(); err != nil {
		t.Fatal(err)
	}
	if original.Hostname == copied.Hostname || original.UUID == copied.UUID || original.MACAddress == copied.MACAddress || original.GatewayMACAddress == copied.GatewayMACAddress {
		t.Fatal("clone reused a source identifier")
	}
	if limayaml.GuestHostname(clone.Config, clone.Name) != copied.Hostname || limayaml.GuestMACAddress(clone.Config, clone.Dir) != copied.MACAddress {
		t.Fatal("guest configuration ignored the saved identity")
	}
	if err := l.Edit(ctx, "project", Settings{CPUs: 2}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := l.inspect(ctx, "project")
	if err != nil || !reflect.DeepEqual(reloaded.Config.GuestIdentity, copied) {
		t.Fatal("resource edit changed guest identity")
	}
	// Recreating the same management name must not recreate its fingerprints.
	if err := l.Delete(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if err := l.Clone(ctx, "base", "project"); err != nil {
		t.Fatal(err)
	}
	recreated, err := l.inspect(ctx, "project")
	if err != nil || recreated.Config.GuestIdentity.UUID == copied.UUID {
		t.Fatal("recreation reused a machine identity")
	}
}

func TestLimaLegacyIdentityIsNotMigratedByEdit(t *testing.T) {
	l := newTestLima(t)
	ctx := t.Context()
	none := []Mount{}
	if err := l.Create(ctx, "legacy", "template:debian-13", Settings{Mounts: &none}, io.Discard); err != nil {
		t.Fatal(err)
	}
	source, err := l.inspect(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.Dir, "lima.yaml")
	// Remove the identity with the same YAML engine used by creation.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err = yqutil.EvaluateExpression(ctx, "del(.guestIdentity)", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.Edit(ctx, "legacy", Settings{CPUs: 2}); err != nil {
		t.Fatal(err)
	}
	legacy, err := l.inspect(ctx, "legacy")
	if err != nil || legacy.Config.GuestIdentity != nil {
		t.Fatal("edited an existing environment's identity")
	}
	if err := l.Clone(ctx, "legacy", "new"); err != nil {
		t.Fatal(err)
	}
	clone, err := l.inspect(ctx, "new")
	if err != nil || clone.Config.GuestIdentity == nil {
		t.Fatal("legacy template clone did not receive an identity")
	}
}
