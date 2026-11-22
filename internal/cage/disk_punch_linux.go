//go:build linux

package cage

import (
	"bytes"
	"context"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

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
		err := unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, runStart, runEnd-runStart)
		if err == nil {
			punched += runEnd - runStart
		} else if err != unix.EOPNOTSUPP && err != unix.ENOSYS && err != unix.EINVAL {
			return err
		}
		runStart = -1
		return nil
	}
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
	return copySingleSparseFile(ctx, src, dst)
}
