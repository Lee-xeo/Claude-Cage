//go:build !windows

package cage

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// DiskImageInfo describes the host-side virtual disk file of a Lima VM.
type DiskImageInfo struct {
	Path               string `json:"-"`
	Format             string `json:"format"`
	VirtualBytes       int64  `json:"virtual_bytes"`
	HostAllocatedBytes int64  `json:"host_allocated_bytes"`
}

// DiskVolumeTotalBytes returns the total byte capacity of the filesystem
// containing dir (walking up to the nearest existing parent when needed).
func DiskVolumeTotalBytes(dir string) (uint64, error) {
	probe := existingParentDir(dir)
	var st syscall.Statfs_t
	if err := syscall.Statfs(probe, &st); err != nil {
		return 0, err
	}
	if st.Bsize <= 0 || st.Blocks == 0 {
		return 0, errors.New("unavailable filesystem capacity")
	}
	return st.Blocks * uint64(st.Bsize), nil
}

// DiskImageUsage reports the virtual capacity and actual host allocated bytes
// of the primary VM disk inside instanceDir.
func DiskImageUsage(instanceDir string) (DiskImageInfo, error) {
	path, err := InstanceDiskPath(instanceDir)
	if err != nil {
		return DiskImageInfo{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return DiskImageInfo{}, err
	}
	format, virtualBytes, err := inspectDiskHeader(path, info.Size())
	if err != nil {
		return DiskImageInfo{}, err
	}
	allocated := info.Size()
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks >= 0 {
		allocated = st.Blocks * 512
	}
	return DiskImageInfo{
		Path:               path,
		Format:             format,
		VirtualBytes:       virtualBytes,
		HostAllocatedBytes: allocated,
	}, nil
}

// InstanceDiskPath locates the active VM disk image in a Lima instance dir,
// resolving Lima v2's "disk" first and falling back to legacy "diffdisk".
func InstanceDiskPath(instanceDir string) (string, error) {
	for _, name := range []string{"disk", "diffdisk"} {
		candidate := filepath.Join(instanceDir, name)
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			if st, statErr := os.Stat(resolved); statErr == nil && st.Mode().IsRegular() {
				return resolved, nil
			}
		}
		if st, statErr := os.Stat(candidate); statErr == nil && st.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func inspectDiskHeader(path string, fileSize int64) (format string, virtualBytes int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	var hdr [32]byte
	n, err := io.ReadFull(f, hdr[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", 0, err
	}
	if n >= 32 && hdr[0] == 'Q' && hdr[1] == 'F' && hdr[2] == 'I' && hdr[3] == 0xfb {
		size := int64(binary.BigEndian.Uint64(hdr[24:32]))
		if size > 0 {
			return "qcow2", size, nil
		}
		return "qcow2", fileSize, nil
	}
	return "raw", fileSize, nil
}

func existingParentDir(dir string) string {
	cur := filepath.Clean(dir)
	for cur != "" {
		if st, err := os.Stat(cur); err == nil && st.IsDir() {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return "/"
}
