package guestprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreationAndReload(t *testing.T) {
	dir := t.TempDir()
	o := Options{Hostname: "MY-PC", Username: "alex", BrowserUsername: "web", AudioName: "speakers", AudioDescription: "Desktop Speakers", DisplayWidth: 1920, DisplayHeight: 1080, BrowserWidth: 1280, BrowserHeight: 720, ExtensionName: "My Tabs", WebGL: "disabled", Canvas: "blocked"}
	p, e := Create(dir, o)
	if e != nil {
		t.Fatal(e)
	}
	if p.Options != o {
		t.Fatalf("manual values changed: %+v", p.Options)
	}
	next, e := Create(dir, Options{})
	if e != nil {
		t.Fatal(e)
	}
	if *p != *next {
		t.Fatal("restart changed profile")
	}
	b, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	var copy Profile
	if e = json.Unmarshal(b, &copy); e != nil || copy != *p {
		t.Fatal("serialization changed profile")
	}
	other, e := New(Options{})
	if e != nil {
		t.Fatal(e)
	}
	if other.ExtensionSuffix == p.ExtensionSuffix || other.Username == p.Username {
		t.Fatal("new profile reused identity")
	}
}

func TestRandomProfiles(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		p, e := New(Options{})
		if e != nil {
			t.Fatal(e)
		}
		if e = p.Validate(); e != nil {
			t.Fatal(e)
		}
		if seen[p.Hostname] || seen[p.Username] || seen[p.ExtensionSuffix] {
			t.Fatal("duplicate generated identity")
		}
		seen[p.Hostname], seen[p.Username], seen[p.ExtensionSuffix] = true, true, true
	}
}

func TestInvalidOptions(t *testing.T) {
	for _, o := range []Options{{Hostname: "bad\nname"}, {Hostname: "-abc"}, {Hostname: "0123"}, {Username: "root"}, {Username: "cage"}, {Username: "a;id"}, {Username: "same", BrowserUsername: "same"}, {AudioName: "a.monitor"}, {AudioDescription: "x\nload-module bad"}, {ExtensionName: "\"; id"}, {DisplayWidth: 1920}, {BrowserWidth: 8000, BrowserHeight: 600}, {DisplayWidth: 800, DisplayHeight: 600, BrowserWidth: 1280, BrowserHeight: 720}, {WebGL: "fake-gpu"}, {Canvas: "unknown"}} {
		if _, e := New(o); e == nil {
			t.Fatalf("accepted invalid options %+v", o)
		}
	}
}

func TestMissingAndCorruptProfiles(t *testing.T) {
	dir := t.TempDir()
	if p, e := Load(dir); e != nil || p != nil {
		t.Fatal("legacy record should be absent")
	}
	path := filepath.Join(dir, Filename)
	for _, raw := range []string{`{}`, `{"version":999}`, `{"unknown":true}`, `{} {}`} {
		if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := Create(dir, Options{}); e == nil {
			t.Fatal("replaced corrupt profile")
		}
		b, _ := os.ReadFile(path)
		if string(b) != raw {
			t.Fatal("overwrote old profile")
		}
	}
}
