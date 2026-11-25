package cage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/paths"
)

// InitialStorageDirectory supplies a fresh packaged Windows installation's
// data location. Profiles and existing storage records keep their own paths.
// Installations inside AppData or Temp (including through directory junctions)
// fall back to the profile's default state directory because Windows AF_UNIX
// sockets under AppData/Temp fail to connect or unlink (#676).
func InitialStorageDirectory(exe string) string {
	resolved, err := paths.Resolve(exe)
	if err != nil {
		return ""
	}
	return initialStorageDirectory(resolved, runtime.GOOS, windowsInitialStorageExcludedRoots())
}

func initialStorageDirectory(exe, platform string, excluded []string) string {
	if platform != "windows" || !strings.EqualFold(filepath.Base(filepath.Dir(exe)), "resources") {
		return ""
	}
	data := windowsApplicationDataDirectory(filepath.Dir(filepath.Dir(exe)), platform)
	if data == "" {
		return ""
	}
	if windowsStoragePathExcluded(data, excluded) {
		return ""
	}
	return data
}

func windowsApplicationDataDirectory(app, platform string) string {
	if platform != "windows" {
		return ""
	}
	marker, err := os.ReadFile(filepath.Join(app, "Cage.install-id"))
	if err != nil || string(marker) != "io.github.lee-xeo.claude-cage" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(app, "Cage.exe")); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return filepath.Join(app, "CageData")
}

func windowsInitialStorageExcludedRoots() []string {
	roots := windowsTargetStorageExcludedRoots()
	for _, temp := range []string{os.TempDir(), os.Getenv("TEMP"), os.Getenv("TMP")} {
		if temp != "" {
			roots = append(roots, temp)
		}
	}
	return roots
}

func windowsTargetStorageExcludedRoots() []string {
	var roots []string
	for _, key := range []string{"LOCALAPPDATA", "APPDATA"} {
		if value := os.Getenv(key); value != "" {
			roots = append(roots, value)
		}
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		roots = append(roots, filepath.Join(home, "AppData"))
	}
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		roots = append(roots, filepath.Join(systemRoot, "Temp"))
	}
	return roots
}

func windowsStoragePathExcluded(candidate string, excluded []string) bool {
	if candidate == "" || len(excluded) == 0 {
		return false
	}
	roots := make([]string, 0, len(excluded)*2)
	for _, root := range excluded {
		if root == "" {
			continue
		}
		roots = append(roots, root)
		if filepath.IsAbs(root) {
			if real, err := StoragePhysicalPath(root); err == nil && real != "" {
				roots = append(roots, real)
			}
		}
	}
	if len(roots) == 0 {
		return false
	}
	if host.WindowsPathWithin(candidate, roots) {
		return true
	}
	if filepath.IsAbs(candidate) {
		if real, err := StoragePhysicalPath(candidate); err == nil && real != "" {
			if host.WindowsPathWithin(real, roots) {
				return true
			}
		} else if parent := filepath.Dir(candidate); parent != candidate {
			if realParent, parentErr := StoragePhysicalPath(parent); parentErr == nil && realParent != "" {
				if host.WindowsPathWithin(realParent, roots) {
					return true
				}
			}
		}
	}
	return false
}
