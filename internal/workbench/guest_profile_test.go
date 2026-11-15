package workbench

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/containerenv"
	"github.com/sylvinus/agent-vm/internal/guestprofile"
)

func TestCreationPreferencesSurviveRetry(t *testing.T) {
	s := &Server{profile: t.TempDir()}
	o := guestprofile.Options{Hostname: "MY-PC", Username: "alex", BrowserUsername: "web"}
	if e := s.prepareGuestProfile(&o, "debian-13"); e != nil {
		t.Fatal(e)
	}
	p, e := guestprofile.Load(s.profile)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.prepareGuestProfile(&o, "debian-13"); e != nil {
		t.Fatal(e)
	}
	if e = s.prepareGuestProfile(nil, "debian-13"); e != nil {
		t.Fatal(e)
	}
	changed := o
	changed.Hostname = "OTHER-PC"
	if e = s.prepareGuestProfile(&changed, "debian-13"); e == nil {
		t.Fatal("changed saved preferences")
	}
	q, e := guestprofile.Load(s.profile)
	if e != nil || *p != *q {
		t.Fatal("retry changed creation identity")
	}
	script, e := s.linuxTemplate("sudo -u cage cat /home/cage/file")
	if e != nil || script != "sudo -u alex cat /home/alex/file" {
		t.Fatal("control command does not follow saved account")
	}
	if !s.linuxPathHasPrefix("/home/alex/.local/files/a", "/home/cage/.local/files/") || s.linuxPathHasPrefix("/home/cage/.local/files/a", "/home/cage/.local/files/") {
		t.Fatal("attachment validator did not follow the configured home")
	}
}

func TestGuestProfilePreviewRequiresTrustedWindow(t *testing.T) {
	s := &Server{profile: t.TempDir()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://localhost/api/environment/guest-profile/preview", strings.NewReader(`{}`))
	s.guestProfileOptions(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted preview accepted")
	}
	p, e := guestprofile.Load(s.profile)
	if e != nil || p != nil {
		t.Fatal("preview persisted an identity")
	}
}

func TestLegacyContainerRetainsItsGuestAccounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX container engine")
	}
	s := &Server{profile: t.TempDir()}
	engine := filepath.Join(t.TempDir(), "podman")
	r, err := containerenv.Configure(s.profile, containerenv.Config{Engine: "podman", Executable: engine, Image: "sha256:" + strings.Repeat("a", 64), CPUs: 1, MemoryBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.prepareGuestProfile(nil, "debian-13"); err == nil {
		t.Fatal("unreachable engine treated as a new container")
	}
	fixture := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
  "container ls") echo existing ;;
  "container inspect") echo '[{"Id":"existing","Config":{"Labels":{"cn.cage.profile":"%s"}}}]' ;;
  "info --format") echo /var/lib/containers ;;
  *) exit 1 ;;
esac
`, strings.TrimPrefix(r.Name(), "cage-"))
	if err := os.WriteFile(engine, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareGuestProfile(nil, "debian-13"); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareGuestProfile(&guestprofile.Options{Username: "alex"}, "debian-13"); err == nil {
		t.Fatal("allowed renaming an existing container account")
	}
	if p, err := guestprofile.Load(s.profile); err != nil || p != nil {
		t.Fatalf("legacy container acquired a new profile: %+v, %v", p, err)
	}
}

func TestReselectValidatesSavedGraphicsWhenPreferencesOmitted(t *testing.T) {
	s := &Server{profile: t.TempDir()}
	p, err := guestprofile.Create(s.profile, guestprofile.Options{GraphicsBackend: "metal"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.prepareGuestProfile(nil, "debian-13"); err == nil {
		t.Fatal("started Linux with the macOS Metal backend")
	}
	if err := s.prepareGuestProfile(nil, "macos"); err != nil {
		t.Fatal(err)
	}
	got, err := guestprofile.Load(s.profile)
	if err != nil || *got != *p {
		t.Fatal("reselection changed the saved profile")
	}
}
