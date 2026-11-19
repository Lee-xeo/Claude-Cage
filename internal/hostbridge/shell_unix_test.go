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
