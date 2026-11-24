package host

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMakeTreeWritableAndRemoveAllWritable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "base-building")
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	cloudConfig := filepath.Join(root, "cloud-config.yaml")
	limaVersion := filepath.Join(nested, "lima-version")
	if err := os.WriteFile(cloudConfig, []byte("#cloud-config\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(limaVersion, []byte("2.0.0"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nested, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = MakeTreeWritable(root)
		_ = os.RemoveAll(root)
	})

	if err := MakeTreeWritable(root); err != nil {
		t.Fatalf("MakeTreeWritable failed: %v", err)
	}
	for _, file := range []string{cloudConfig, limaVersion} {
		info, err := os.Lstat(file)
		if err != nil {
			t.Fatalf("Lstat %s: %v", file, err)
		}
		if info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("%s remained read-only: %v", file, info.Mode())
		}
	}

	// Re-apply read-only bits to exercise RemoveAllWritable end-to-end.
	if err := os.Chmod(cloudConfig, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(limaVersion, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nested, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAllWritable(root); err != nil {
		t.Fatalf("RemoveAllWritable failed: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("directory still exists after RemoveAllWritable: %v", err)
	}
}

func TestRemoveAllWritableDoesNotModifySymlinkTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Unprivileged Windows accounts may not have SeCreateSymbolicLinkPrivilege.
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	base := t.TempDir()
	outsideFile := filepath.Join(base, "outside.yaml")
	if err := os.WriteFile(outsideFile, []byte("keep"), 0o444); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(base, "outside-dir")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideNested := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideNested, []byte("keep"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outsideDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(outsideDir, 0o700)
		_ = os.Chmod(outsideNested, 0o600)
		_ = os.Chmod(outsideFile, 0o600)
	})

	victim := filepath.Join(base, "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(victim, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(victim, "dir-link")); err != nil {
		t.Fatal(err)
	}
	// Make victim read-only so RemoveAllWritable takes the MakeTreeWritable path.
	if err := os.Chmod(victim, 0o500); err != nil {
		t.Fatal(err)
	}

	if err := RemoveAllWritable(victim); err != nil {
		t.Fatalf("RemoveAllWritable failed: %v", err)
	}
	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Fatalf("victim still exists: %v", err)
	}
	info, err := os.Lstat(outsideFile)
	if err != nil {
		t.Fatalf("outside file disappeared: %v", err)
	}
	if info.Mode().Perm()&0o200 != 0 {
		t.Fatalf("outside symlink target became writable: %v", info.Mode())
	}
	dirInfo, err := os.Lstat(outsideDir)
	if err != nil {
		t.Fatalf("outside dir disappeared: %v", err)
	}
	if dirInfo.Mode().Perm()&0o200 != 0 {
		t.Fatalf("outside dir target became writable: %v", dirInfo.Mode())
	}
}

func TestRemoveAllWritableMissingAndEmptyPath(t *testing.T) {
	if err := RemoveAllWritable(""); err != nil {
		t.Fatalf("empty path: %v", err)
	}
	if err := MakeTreeWritable(""); err != nil {
		t.Fatalf("empty path MakeTreeWritable: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := RemoveAllWritable(missing); err != nil {
		t.Fatalf("missing path: %v", err)
	}
	if err := MakeTreeWritable(missing); err != nil {
		t.Fatalf("missing path MakeTreeWritable: %v", err)
	}
}
