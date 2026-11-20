package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSubcommandHelpDoesNotRunTheCommand(t *testing.T) {
	config := filepath.Join(t.TempDir(), "profile", "config.json")
	t.Setenv("CLAUDE_CAGE_CONFIG", config)
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	for _, flag := range []string{"--help", "-h"} {
		for _, command := range []string{"init", "setup", "up", "serve", "prepare-update", "version"} {
			os.Args = []string{"cage", command, flag}
			if code := run(); code != 0 {
				t.Fatalf("%s %s returned %d", command, flag, code)
			}
		}
	}
	if _, err := os.Stat(filepath.Dir(config)); !os.IsNotExist(err) {
		t.Fatalf("help created the profile: %v", err)
	}
}

func TestSubcommandHelpText(t *testing.T) {
	text, ok := subcommandHelp([]string{"stop", "--help"})
	if !ok || !strings.HasPrefix(text, "cage stop ") || !strings.Contains(text, "--keep-proxy") || strings.Contains(text, "cage list") {
		t.Fatalf("stop help: %q", text)
	}
	if text, ok = subcommandHelp([]string{"prepare-update", "-h"}); !ok || text != help {
		t.Fatalf("an undocumented command falls back to the overview: %q", text)
	}
	// Arguments after these commands belong to the program they run, or the
	// command answers --help itself.
	for _, args := range [][]string{{"run", "--help"}, {"claude", "-h"}, {"agent", "--help"}, {"run", "ls", "-h"}, {"init"}, {"init", "--force", "--help"}} {
		if _, ok := subcommandHelp(args); ok {
			t.Fatalf("%v must not be answered as Cage help", args)
		}
	}
}

func TestSetupPreinstallKeepsWorkbenchTools(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"--preinstall=default"}, []string{"--preinstall=default,chromium,code-server"}},
		{[]string{"--preinstall=python,code-server", "--disk", "30"}, []string{"--preinstall=python,code-server,chromium", "--disk", "30"}},
		{[]string{"--preinstall", "--disk", "30"}, []string{"--preinstall=default,chromium,code-server", "--disk", "30"}},
		{[]string{"--preinstall", "none"}, []string{"--preinstall=none,chromium,code-server"}},
		{[]string{"--preinstall="}, []string{"--preinstall=default,chromium,code-server"}},
		{[]string{"--disk", "30"}, []string{"--disk", "30"}},
	} {
		if got := requireWorkbenchTools(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
}
