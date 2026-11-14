package host

import (
	"al.essio.dev/pkg/shellescape"
	"os"
	"path/filepath"
)

// ConfigureWindowsRuntime retains the existing cross-platform entry point.
// On Intel Macs it scopes the bundled QEMU runtime to this Cage process.
func ConfigureWindowsRuntime() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(exe), "intel-macos", "runtime", "bin")
	if info, err := os.Stat(filepath.Join(dir, "qemu-system-x86_64")); err == nil && info.Mode().IsRegular() {
		if err := os.Setenv("QEMU_SYSTEM_X86_64", shellescape.Quote(filepath.Join(dir, "qemu-system-x86_64"))); err != nil {
			return err
		}
		return os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return nil
}
