package guestprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudioCatalogAndGeneratedDevice(t *testing.T) {
	models := AudioModels()
	if len(models) < 12 {
		t.Fatal("audio catalog is incomplete")
	}
	seen := map[string]bool{}
	for _, m := range models {
		if m.ID == "" || seen[m.ID] || !strings.HasPrefix(m.Source, "https://") {
			t.Fatal("invalid catalog provenance")
		}
		seen[m.ID] = true
		p, err := New(Options{AudioModel: m.ID})
		if err != nil {
			t.Fatal(err)
		}
		if p.AudioDescription != m.Label || !strings.HasPrefix(p.AudioName, "alsa_output.usb-"+m.Stem+"_") || !strings.HasSuffix(p.AudioName, "-00.analog-stereo") {
			t.Fatal("generated audio identity differs from selected real model")
		}
	}
}

func TestCustomHomesAndPlatformGraphics(t *testing.T) {
	p, err := New(Options{Username: "alex", BrowserUsername: "sam", WorkHome: "/home/projects", BrowserHome: "/home/browsing", GraphicsBackend: "opengl"})
	if err != nil {
		t.Fatal(err)
	}
	script := p.Linux("User=cage\nUser=cage-browser\ncat /home/cage/file /home/cage-browser/file\nsudo useradd --create-home --shell /bin/bash cage-browser\n")
	for _, expected := range []string{"/home/projects/file", "/home/browsing/file", "--home-dir /home/browsing"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("missing custom home %s", expected)
		}
	}
	if p.Linux(script) != script {
		t.Fatal("custom home rendering changed on reconnect")
	}
	for _, o := range []Options{{WorkHome: "/root/private"}, {BrowserHome: "/home/a/../b"}, {WorkHome: "/home/same", BrowserHome: "/home/same"}, {Username: "alex", BrowserUsername: "sam", WorkHome: "/home/sam"}, {AudioModel: "missing"}, {AudioModel: "ur22c", AudioDescription: "other"}, {GPUBackend: "virgl", GraphicsBackend: "vulkan"}} {
		if _, err := New(o); err == nil {
			t.Fatalf("accepted incompatible profile %+v", o)
		}
	}
	for _, item := range []struct {
		backend, platform string
		allowed           bool
	}{{"metal", "darwin", true}, {"metal", "linux", false}, {"d3d11", "windows", true}, {"d3d11", "darwin", false}, {"vulkan", "linux", true}, {"vulkan", "windows", false}, {"opengl", "linux", true}} {
		err := (Options{GraphicsBackend: item.backend}).ValidatePlatform(item.platform)
		if (err == nil) != item.allowed {
			t.Fatal("incorrect platform/backend applicability")
		}
	}
}

func TestLegacyProfileWithoutNewFields(t *testing.T) {
	p, err := New(Options{Username: "legacy", BrowserUsername: "legacyweb", AudioName: "old_sink", AudioDescription: "Speakers"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, Filename), b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AudioName != "old_sink" || loaded.WorkHomePath() != "/home/legacy" || len(loaded.GraphicsFlags()) != 0 {
		t.Fatal("legacy profile was migrated unexpectedly")
	}
}

func TestReservedImageHomesAreRejected(t *testing.T) {
	for _, home := range []string{"/home/cage", "/home/cage-browser"} {
		if _, err := New(Options{WorkHome: home}); err == nil {
			t.Fatal("accepted a home path reserved for base-image provisioning")
		}
		if _, err := New(Options{BrowserHome: home}); err == nil {
			t.Fatal("accepted a browser home path reserved for base-image provisioning")
		}
	}
}
