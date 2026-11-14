package cage

import (
	"github.com/sylvinus/agent-vm/internal/guestprofile"
	"os/exec"
	"strings"
	"testing"
)

func TestConfiguredGuestScripts(t *testing.T) {
	p, e := guestprofile.New(guestprofile.Options{Username: "alex", BrowserUsername: "web", AudioName: "speaker123", AudioDescription: "USB Speakers", ExtensionName: "My Tab Layout", DisplayWidth: 1920, DisplayHeight: 1080, BrowserWidth: 1280, BrowserHeight: 720, WebGL: "disabled", Canvas: "blocked"})
	if e != nil {
		t.Fatal(e)
	}
	c := DefaultConfig()
	c.GuestProfile = p
	for name, script := range map[string]string{"setup": c.SetupScript(), "start": c.StartScript()} {
		for _, bad := range []string{"/home/cage", "User=cage\n", "User=cage-browser\n", "-u cage-browser ", "-u cage ", "cage-browser ALL=", "getpwnam('cage')", "getpwnam('cage-browser')", "cage_output", "2880x1800", "--window-size=1440,900", "Cage browser panes"} {
			if strings.Contains(script, bad) {
				t.Fatalf("%s retains %q", name, bad)
			}
		}
		for _, want := range []string{"User=web\n", "/home/alex", "--window-size=1280,720", "--disable-reading-from-canvas", "--disable-webgl", "browser-panes-" + p.ExtensionSuffix, `"name": "My Tab Layout"`, "set-default-source speaker123.monitor", "1920x1080"} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s misses %q", name, want)
			}
		}
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s invalid: %v %s", name, e, b)
		}
	}
}
