package workbench

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
)

func TestAutomaticRegionSettingsPreserveManualOverrides(t *testing.T) {
	cfg := cage.DefaultConfig()
	loc := Location{CountryCode: "JP", Timezone: "Asia/Tokyo"}
	manual := autoRegionSettings(cfg, Preferences{}, loc)
	if manual != cfg {
		t.Fatal("location changed manually selected settings")
	}
	tzOnly := autoRegionSettings(cfg, Preferences{AutoTimezone: true}, loc)
	if tzOnly.Timezone != "Asia/Tokyo" || tzOnly.Locale != cfg.Locale {
		t.Fatal("timezone option changed language")
	}
	languageOnly := autoRegionSettings(cfg, Preferences{AutoLanguage: true}, loc)
	if languageOnly.Locale != "ja_JP.UTF-8" || languageOnly.BrowserLanguage != "ja-JP" || languageOnly.Timezone != cfg.Timezone {
		t.Fatal("language option did not preserve independent timezone")
	}
	unknown := autoRegionSettings(cfg, Preferences{AutoLanguage: true}, Location{CountryCode: "ZZ"})
	if unknown != cfg {
		t.Fatal("unknown region invented a language setting")
	}
}

// The node timezone reaches login shells through the namespace's environment
// file, set up as root before privileges drop, and never as a CAGE_ variable.
func TestAccountTimezoneEnvironmentRunsBeforePrivilegeDrop(t *testing.T) {
	setup := accountTimezoneEnvironment("America/New_York")
	if strings.Contains(setup, "CAGE_") {
		t.Fatalf("setup exposes a Cage variable:\n%s", setup)
	}
	wrapped := wrapAccountNamespace("ip netns exec net-a unshare -m", setup, "true")
	mount, drop := strings.Index(wrapped, "mount --bind \"$account_environment\""), strings.Index(wrapped, "setpriv")
	if mount < 0 || drop < mount {
		t.Fatalf("environment is not replaced before setpriv:\n%s", wrapped)
	}
	if plain := wrapAccountNamespace("ip netns exec net-a unshare -m", "", "true"); strings.Contains(plain, "claude-cage/environment") {
		t.Fatalf("account without a node timezone replaced the environment:\n%s", plain)
	}
}

func TestAccountTimezoneEnvironmentReplacesOnlyTimezone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses GNU coreutils as in the guest")
	}
	cases := []struct {
		name, mount, want string
	}{
		{"mounted", `cp "$2" "$3"`, "LANG=zh_CN.UTF-8\nLC_ALL=zh_CN.UTF-8\nBROWSER=/usr/local/bin/cage-open\nTZ=America/New_York\n"},
		{"mount fails", "exit 32", "TZ=Asia/Shanghai\nLANG=zh_CN.UTF-8\nLC_ALL=zh_CN.UTF-8\nBROWSER=/usr/local/bin/cage-open"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			bin, run := filepath.Join(dir, "bin"), filepath.Join(dir, "run")
			for _, d := range []string{bin, run} {
				if err := os.Mkdir(d, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "mount"), []byte("#!/bin/sh\n"+c.mount+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			environment := filepath.Join(dir, "environment")
			// Existing guests have the file without a final newline.
			if err := os.WriteFile(environment, []byte("TZ=Asia/Shanghai\nLANG=zh_CN.UTF-8\nLC_ALL=zh_CN.UTF-8\nBROWSER=/usr/local/bin/cage-open"), 0644); err != nil {
				t.Fatal(err)
			}
			script := strings.NewReplacer("/etc/claude-cage/environment", environment, "/run/", run+"/").Replace(accountTimezoneEnvironment("America/New_York"))
			cmd := exec.Command("bash", "-c", "set -eu\n"+script)
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			got, err := os.ReadFile(environment)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("environment = %q, want %q", got, c.want)
			}
			if left, _ := os.ReadDir(run); len(left) != 0 {
				t.Fatalf("temporary environment left behind: %v", left)
			}
		})
	}
}

func TestMissingExitPolicyDoesNotRelaxSavedStaticRule(t *testing.T) {
	s := testServer(t)
	old := cage.DefaultConfig()
	old.ExitPolicy = cage.ExitPolicy{Mode: "static", Scope: "country", IP: "8.8.8.8"}
	input := settingsView(old, Preferences{Keyboard: "mac"})
	input.ExitPolicy = cage.ExitPolicy{}
	got, _, err := proposedSettings(input, old, s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitPolicy != old.ExitPolicy {
		t.Fatal("legacy form silently relaxed static IP")
	}
}
