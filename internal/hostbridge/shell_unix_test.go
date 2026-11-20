//go:build !windows

package hostbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTerminalPathTakesThePathOfTheLoginShell(t *testing.T) {
	// A shell whose start-up prints noise and extends PATH, as users' do.
	shell := filepath.Join(t.TempDir(), "shell")
	script := "#!/bin/sh\necho welcome\nPATH=\"/opt/tools/bin:$PATH\"\nshift 2\nexec /bin/sh \"$@\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", "/usr/bin:/bin")
	if path := readTerminalPath(); path != "/opt/tools/bin:/usr/bin:/bin" {
		t.Fatalf("PATH not taken from the shell: %q", path)
	}
}

func TestLookProgramSearchesThePathGivenToTheProgram(t *testing.T) {
	// Tools such as npx or uvx are often only on the terminal's PATH.
	tools, empty := t.TempDir(), t.TempDir()
	program := filepath.Join(tools, "runner")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empty, "runner"), []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=/home/someone", "PATH=relative:" + empty + ":" + tools}
	if found := lookProgram("runner", env); found != program {
		t.Fatalf("found %q, want %q", found, program)
	}
	if found := lookProgram("missing", env); found != "missing" {
		t.Fatalf("a program on no PATH folder should be left to exec, got %q", found)
	}
}
