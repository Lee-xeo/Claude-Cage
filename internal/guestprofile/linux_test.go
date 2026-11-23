package guestprofile

import (
	"os/exec"
	"strings"
	"testing"
)

func TestLinuxAccountsAndRoles(t *testing.T) {
	p, e := New(Options{Username: "alex", BrowserUsername: "web"})
	if e != nil {
		t.Fatal(e)
	}
	s := p.Linux("User=cage-browser\nGroup=cage-audio\nUser=cage\nsudo -H -u cage cat /home/cage/file\nsudo useradd --create-home --shell /bin/bash cage-browser\ncage-browser ALL=(cage) NOPASSWD: /usr/local/bin/tool\nsystemctl restart cage-browser.service\nmessage['source'] = 'cage-browser'\naccount = pwd.getpwnam(user)\n")
	for _, want := range []string{"User=web\n", "Group=cage-audio\n", "User=alex\n", "-u alex cat /home/alex/file", "/bin/bash web", "web ALL=(alex)", "restart cage-browser.service", "message['source'] = 'cage-browser'", "'cage': 'alex', 'cage-browser': 'web'"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if p.Linux(s) != s {
		t.Fatal("rendering is not idempotent")
	}
}

func TestLinuxDimensionsAndBrowser(t *testing.T) {
	p, e := New(Options{AudioName: "speakers", AudioDescription: "My Audio", ExtensionName: "My Tabs", WebGL: "disabled", Canvas: "blocked"})
	if e != nil {
		t.Fatal(e)
	}
	s := p.Linux(`load-module module-null-sink sink_name=cage_output sink_properties=device.description=Cage
load-module module-pipe-source source_name=cage_input file=/run/cage-audio/input.fifo format=s16le rate=48000 channels=1 source_properties=device.description=Cage
set-default-source cage_input
const flags = ["--no-first-run", "--window-size=1440,900"];
const directory = "/usr/local/lib/cage-split";
{"name": "Cage browser panes"}
`)
	for _, want := range []string{"sink_name=speakers", "source_name=speakers_input", `source_properties="device.description='My Audio'"`, "set-default-source speakers_input", "--disable-webgl", "--disable-reading-from-canvas", "browser-panes-" + p.ExtensionSuffix, `"name": "My Tabs"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if p.Linux(s) != s {
		t.Fatal("browser flags duplicated")
	}
	usb, err := New(Options{AudioModel: "scarlett-solo-4"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(usb.AudioInputName(), "alsa_input.usb-Focusrite_Scarlett_Solo_") {
		t.Fatalf("unexpected USB audio input name: %q", usb.AudioInputName())
	}
}

func TestLiteralOptionsDoNotChangeShellGrammar(t *testing.T) {
	p, e := New(Options{Username: "alpha", BrowserUsername: "beta", AudioDescription: "Desktop (USB)", ExtensionName: "Tab Tools"})
	if e != nil {
		t.Fatal(e)
	}
	s := p.Linux("sudo -u cage-browser true\ncat <<'PULSE'\nload-module module-null-sink sink_name=cage_output sink_properties=device.description=Cage\nPULSE\n")
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(s)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("invalid generated shell: %v %s", e, b)
	}
}
