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
	ptr, err := windows.UTF16PtrFromString(cloudConfig)
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
	}
	// Confirm plain os.RemoveAll reproduces the Windows ReadOnly failure.
	if err := os.RemoveAll(dir); err == nil {
		t.Fatal("expected plain os.RemoveAll to fail on Windows FILE_ATTRIBUTE_READONLY file")
	}
	if err := RemoveAllWritable(dir); err != nil {
		t.Fatalf("RemoveAllWritable failed on Windows read-only cloud-config.yaml: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("base-building still exists after RemoveAllWritable: %v", err)
	}
}
