//go:build windows

package cage

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DiskImageInfo describes the host-side virtual disk file of a Lima VM.
type DiskImageInfo struct {
	Path               string `json:"-"`
	Format             string `json:"format"`
	VirtualBytes       int64  `json:"virtual_bytes"`
	HostAllocatedBytes int64  `json:"host_allocated_bytes"`
}

var (
	kernel32DiskStats          = syscall.NewLazyDLL("kernel32.dll")
	procGetCompressedFileSizeW = kernel32DiskStats.NewProc("GetCompressedFileSizeW")
)

// DiskVolumeTotalBytes returns the total byte capacity of the filesystem
// containing dir.
func DiskVolumeTotalBytes(dir string) (uint64, error) {
	probe := existingParentDir(dir)
	ptr, err := windows.UTF16PtrFromString(probe)
	if err != nil {
		return 0, err
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeAvail, &total, &totalFree); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, errors.New("unavailable filesystem capacity")
	}
	return total, nil
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
	if compressed, cErr := compressedFileSize(path); cErr == nil && compressed >= 0 {
		allocated = compressed
	}
	return DiskImageInfo{
		Path:               path,
		Format:             format,
		VirtualBytes:       virtualBytes,
		HostAllocatedBytes: allocated,
	}, nil
}

func compressedFileSize(path string) (int64, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var high uint32
	low, _, callErr := procGetCompressedFileSizeW.Call(uintptr(unsafe.Pointer(ptr)), uintptr(unsafe.Pointer(&high)))
	if low == 0xffffffff && callErr != nil && callErr != windows.ERROR_SUCCESS {
		return 0, callErr
	}
	return int64(uint64(high)<<32 | uint64(uint32(low))), nil
}

// InstanceDiskPath locates the active VM disk image in a Lima instance dir.
func InstanceDiskPath(instanceDir string) (string, error) {
	for _, name := range []string{"disk", "diffdisk"} {
		candidate := filepath.Join(instanceDir, name)
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
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
	return `C:\`
}
