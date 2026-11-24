package host

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRemoveAllWritableClearsWindowsReadOnlyAttribute(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "base-building")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cloudConfig := filepath.Join(dir, "cloud-config.yaml")
	if err := os.WriteFile(cloudConfig, []byte("#cloud-config\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	limaVersion := filepath.Join(dir, "lima-version")
	if err := os.WriteFile(limaVersion, []byte("2.0.0"), 0o444); err != nil {
		t.Fatal(err)
	}
	setReadOnly := func(path string) *uint16 {
		t.Helper()
		ptr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		attrs, err := windows.GetFileAttributes(ptr)
		if err != nil {
			t.Fatal(err)
		}
		if attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
			if err := windows.SetFileAttributes(ptr, attrs|windows.FILE_ATTRIBUTE_READONLY); err != nil {
				t.Fatal(err)
			}
			attrs, err = windows.GetFileAttributes(ptr)
			if err != nil {
				t.Fatal(err)
			}
		}
		if attrs&windows.FILE_ATTRIBUTE_READONLY == 0 {
			t.Fatalf("expected FILE_ATTRIBUTE_READONLY on %s", path)
		}
		return ptr
	}

	cloudPtr := setReadOnly(cloudConfig)
	verPtr := setReadOnly(limaVersion)

	if err := MakeTreeWritable(dir); err != nil {
		t.Fatalf("MakeTreeWritable failed: %v", err)
	}
	for name, ptr := range map[string]*uint16{"cloud-config.yaml": cloudPtr, "lima-version": verPtr} {
		attrs, err := windows.GetFileAttributes(ptr)
		if err != nil {
			t.Fatalf("GetFileAttributes(%s): %v", name, err)
		}
		if attrs&windows.FILE_ATTRIBUTE_READONLY != 0 {
			t.Fatalf("%s retained FILE_ATTRIBUTE_READONLY after MakeTreeWritable", name)
		}
	}

	// Re-apply DOS ReadOnly attributes and verify RemoveAllWritable removes the tree.
	setReadOnly(cloudConfig)
	setReadOnly(limaVersion)
	if err := RemoveAllWritable(dir); err != nil {
		t.Fatalf("RemoveAllWritable failed on Windows read-only base-building: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("base-building still exists after RemoveAllWritable: %v", err)
	}
}
