package cage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lima-vm/lima/v2/pkg/limatype/dirnames"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/osutil"
	"github.com/sylvinus/agent-vm/internal/vmname"
)

func TestEnvironmentIdentitySurvivesProjectSelectionAndLegacyUpgrade(t *testing.T) {
	profile := t.TempDir()
	legacy, _ := filepath.EvalSymlinks(t.TempDir())
	prefs, _ := json.Marshal(map[string]string{"workspace": legacy})
	if err := os.WriteFile(filepath.Join(profile, "workbench.json"), prefs, 0600); err != nil {
		t.Fatal(err)
	}
	if EnvironmentDir(profile) == legacy {
		t.Fatal("selected project without an existing VM became the environment")
	}
	vmDir := filepath.Join(profile, "state", "lima", InstanceName(profile, legacy))
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, "lima.yaml"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if EnvironmentDir(profile) != legacy {
		t.Fatal("existing VM disk was not retained during upgrade")
	}
	saved, _ := json.Marshal(map[string]string{"directory": legacy})
	if err := os.WriteFile(filepath.Join(profile, "environment.json"), saved, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "workbench.json"), []byte(`{"workspace":"/different-project"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if EnvironmentDir(profile) != legacy {
		t.Fatal("changing project changed the environment VM")
	}
}

func TestLimaSocketPathBoundary(t *testing.T) {
	profile := t.TempDir()
	root := filepath.VolumeName(profile) + string(filepath.Separator)
	for _, name := range []string{vmname.Building, vmname.Template, InstanceName(profile, EnvironmentDir(profile))} {
		t.Run(name, func(t *testing.T) {
			n := osutil.UnixPathMax - len(filepath.Join(root, "x", name, filenames.LongestSock))
			// The first component includes a multibyte character to check bytes.
			fits := filepath.Join(root, "界"+strings.Repeat("x", n-3), name)
			if err := ValidateLimaSocketPath(fits); err != nil {
				t.Fatal(err)
			}
			atLimit := filepath.Join(root, "界"+strings.Repeat("x", n-2), name)
			err := ValidateLimaSocketPath(atLimit)
			if !errors.Is(err, ErrLimaSocketPathTooLong) ||
				!strings.Contains(err.Error(), fmt.Sprintf("需要 %d 字节，最多 %d 字节", osutil.UnixPathMax, osutil.UnixPathMax-1)) ||
				strings.Contains(err.Error(), atLimit) {
				t.Fatalf("expected a path-free boundary error, got %v", err)
			}
		})
	}
}

func TestProfileLimaHomeStorageLinks(t *testing.T) {
	// Leave room in the socket budget for the logical profile used on Windows.
	base := os.TempDir()
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	root := filepath.Join(base, "lc"+strconv.FormatInt(int64(os.Getpid()), 36))
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	profile, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	logical := filepath.Join(profile, "state", "lima")
	if err := ValidateLimaSocketPath(filepath.Join(logical, vmname.Building)); err != nil {
		t.Skip("this host's test temp root does not leave room for a short logical Lima home")
	}
	target := filepath.Join(t.TempDir(), strings.Repeat("physical-storage-", 5))
	targetState := filepath.Join(target, "state")
	if err := ValidateStorageTarget(profile, target); err != nil {
		t.Fatalf("deep physical destination should be selectable: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "profile-link")
	if err := linkStorage(alias, profile); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetState, 0700); err != nil {
		t.Fatal(err)
	}
	if err := linkStorage(storageAnchor(profile), targetState); err != nil {
		t.Fatal(err)
	}
	realState, err := filepath.EvalSymlinks(targetState)
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(realState, "lima")

	// An unmanaged link uses the physical spelling even before lima exists.
	if home, err := ProfileLimaHome(alias, false); err != nil || home != physical {
		t.Fatalf("unmanaged Lima home: %q, %v; want %q", home, err, physical)
	}
	if err := ValidateProfileLimaSocketPaths(alias); !errors.Is(err, ErrLimaSocketPathTooLong) ||
		!strings.Contains(err.Error(), fmt.Sprintf("需要 %d 字节", len(filepath.Join(physical, vmname.Building, filenames.LongestSock)))) {
		t.Fatalf("unmanaged link should report the physical path's length: %v", err)
	}
	if err := os.Mkdir(physical, 0700); err != nil {
		t.Fatal(err)
	}
	home, err := ProfileLimaHome(alias, false)
	if err != nil || home != physical {
		t.Fatalf("unmanaged Lima home after creation: %q, %v; want %q", home, err, physical)
	}
	t.Setenv("LIMA_HOME", home)
	t.Setenv("CAGE_LOGICAL_LIMA_HOME", logical)
	if got, err := dirnames.LimaDir(); err != nil || got != physical {
		t.Fatalf("Lima without a matching logical home: %q, %v; want %q", got, err, physical)
	}

	// A Cage-managed link keeps the resolved profile prefix, even through an
	// alias and even though the physical destination would exceed the limit.
	id := strings.Repeat("a", 32)
	if err := os.WriteFile(filepath.Join(target, storageMarker), []byte(id), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeStorageJSON(filepath.Join(profile, locationFile), storageRecord{Directory: target, ID: id}); err != nil {
		t.Fatal(err)
	}
	loc, err := GetStorageLocation(alias)
	if err != nil || !loc.Custom {
		t.Fatalf("custom storage fixture: %+v, %v", loc, err)
	}
	home, err = ProfileLimaHome(alias, loc.Custom)
	if err != nil || home != logical {
		t.Fatalf("custom Lima home: %q, %v; want %q", home, err, logical)
	}
	t.Setenv("LIMA_HOME", home)
	t.Setenv("CAGE_LOGICAL_LIMA_HOME", home)
	if got, err := dirnames.LimaDir(); err != nil || got != home {
		t.Fatalf("Lima with the logical home: %q, %v; want %q", got, err, home)
	}
	if err := ValidateProfileLimaSocketPaths(alias); err != nil {
		t.Fatalf("custom storage should use the short logical home: %v", err)
	}

	// A restored workspace can retain a name longer than today's name budget.
	workspace := filepath.Join(profile, strings.Repeat("workspace", 8))
	if err := writeStorageJSON(filepath.Join(profile, "environment.json"), EnvironmentRecord{Directory: workspace}); err != nil {
		t.Fatal(err)
	}
	oldName := vmname.Name(workspace)
	oldDir := filepath.Join(physical, oldName)
	if err := os.Mkdir(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, filenames.LimaYAML), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := InstanceName(alias, EnvironmentDir(alias)); got != oldName {
		t.Fatalf("fixture did not retain the restored instance name: %q", got)
	}
	if err := ValidateProfileLimaSocketPaths(alias); !errors.Is(err, ErrLimaSocketPathTooLong) ||
		!strings.Contains(err.Error(), fmt.Sprintf("需要 %d 字节", len(filepath.Join(logical, oldName, filenames.LongestSock)))) {
		t.Fatalf("validation should include the restored instance name: %v", err)
	}
}
