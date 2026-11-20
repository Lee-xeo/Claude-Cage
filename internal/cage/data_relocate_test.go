//go:build !windows

package cage

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The relocation of the data directory: every byte, mode, time and file
// identity kept; repeatable; interrupted runs, data already at the current
// name, a failed check and a directory that cannot be renamed.
func TestDataRelocation(t *testing.T) {
	cmd := exec.Command(python3(t), "-I", "data-relocate.test.py")
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("data relocation: %v\n%s", err, output)
	}
}

// The relocation runs first, as the work account, before the migration of
// settings and before any current program is installed.
func TestDataRelocationRunsFirst(t *testing.T) {
	start := DefaultConfig().StartScript()
	relocation := strings.Index(start, "<<'DATA_RELOCATE'")
	if relocation < 0 {
		t.Fatal("the start script does not relocate the data directory")
	}
	line := start[strings.LastIndex(start[:relocation], "\n")+1 : relocation]
	if strings.Contains(line, "sudo") || !strings.HasPrefix(line, "python3 -I - ") {
		t.Fatalf("relocation must run as the work account: %q", line)
	}
	for _, later := range []string{"legacy_copy=", "/usr/local/libexec/claude-account", "systemctl stop"} {
		if index := strings.Index(start, later); index < relocation {
			t.Fatalf("%q runs before the data directory is relocated", later)
		}
	}
}
