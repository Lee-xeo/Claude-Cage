//go:build darwin

package cage

import (
	"bytes"
	"context"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

type darwinPunchHole struct {
	flags    uint32
	reserved uint32
	offset   int64
	length   int64
}

// punchZeroHoles scans a cold raw disk image in 1 MiB aligned chunks and
// releases fully-zero runs back to APFS via F_PUNCHHOLE.
func punchZeroHoles(ctx context.Context, path string) (int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	const chunkSize = 1 << 20
	buf := make([]byte, chunkSize)
	zeros := make([]byte, chunkSize)
	var punched int64
	runStart := int64(-1)
	flush := func(runEnd int64) error {
		if runStart < 0 || runEnd <= runStart {
			runStart = -1
			return nil
		}
		ph := darwinPunchHole{offset: runStart, length: runEnd - runStart}
		_, _, errno := unix.Syscall(unix.SYS_FCNTL, f.Fd(), uintptr(unix.F_PUNCHHOLE), uintptr(unsafe.Pointer(&ph)))
		if errno == 0 {
			punched += runEnd - runStart
		} else if errno != unix.ENOTSUP && errno != unix.EINVAL && errno != unix.EOPNOTSUPP {
			return errno
		}
		runStart = -1
		return nil
	}
	// Never touch LBA 0..33 (first 1 MiB) or the last 1 MiB (Backup GPT).
	limit := info.Size() - chunkSize
	for off := int64(chunkSize); off+chunkSize <= limit; off += chunkSize {
		if err := ctx.Err(); err != nil {
			return punched, err
		}
		n, rErr := f.ReadAt(buf, off)
		if rErr != nil && rErr != io.EOF {
			return punched, rErr
		}
		if n == chunkSize && bytes.Equal(buf, zeros) {
			if runStart < 0 {
				runStart = off
			}
			continue
		}
		if err := flush(off); err != nil {
			return punched, err
		}
	}
	if err := flush((limit / chunkSize) * chunkSize); err != nil {
		return punched, err
	}
	return punched, f.Sync()
}

func cloneDiskBackup(ctx context.Context, src, dst string) error {
	_ = os.Remove(dst)
	if err := unix.Clonefile(src, dst, 0); err == nil {
		return nil
	}
	return copySingleSparseFile(ctx, src, dst)
}
