package host

import (
	"errors"
	"os"
	"path/filepath"
)

// MakeTreeWritable clears read-only permission bits (including Windows DOS
// ReadOnly) on root and regular files or directories within it, without
// following symlinks, Windows directory junctions, or other reparse points.
func MakeTreeWritable(root string) error {
	if root == "" {
		return nil
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return makeEntryWritable(root, info)
}

func makeEntryWritable(path string, info os.FileInfo) error {
	// Never follow symlinks, junctions, or other reparse points.
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return nil
	}
	if !info.IsDir() {
		if info.Mode().Perm()&0o200 == 0 {
			if err := os.Chmod(path, info.Mode().Perm()|0o200); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	var firstErr error
	if info.Mode().Perm()&0o700 != 0o700 {
		if err := os.Chmod(path, info.Mode().Perm()|0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
			firstErr = err
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return firstErr
		}
		if firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		childInfo, err := os.Lstat(child)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := makeEntryWritable(child, childInfo); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RemoveAllWritable removes path and any children it contains. If the initial
// os.RemoveAll fails (for example due to Windows DOS ReadOnly files like
// Lima's 0o444 cloud-config.yaml/lima-version or read-only subdirectories on
// Unix), it makes entries inside path writable without following symlinks or
// junctions and retries removal.
func RemoveAllWritable(path string) error {
	if path == "" {
		return nil
	}
	err := os.RemoveAll(path)
	if err == nil {
		return nil
	}
	_ = MakeTreeWritable(path)
	return os.RemoveAll(path)
}
