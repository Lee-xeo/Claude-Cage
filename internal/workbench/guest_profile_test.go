package workbench

import (
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreationPreferencesSurviveRetry(t *testing.T) {
	s := &Server{profile: t.TempDir()}
	o := guestprofile.Options{Hostname: "MY-PC", Username: "alex", BrowserUsername: "web"}
	if e := s.prepareGuestProfile(&o); e != nil {
		t.Fatal(e)
	}
	p, e := guestprofile.Load(s.profile)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.prepareGuestProfile(&o); e != nil {
		t.Fatal(e)
	}
	if e = s.prepareGuestProfile(nil); e != nil {
		t.Fatal(e)
	}
	changed := o
	changed.Hostname = "OTHER-PC"
	if e = s.prepareGuestProfile(&changed); e == nil {
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
