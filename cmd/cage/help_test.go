package main

import (
	"os"
	"path/filepath"
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
