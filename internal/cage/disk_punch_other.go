//go:build !darwin && !linux

package cage

import (
	"context"
	"os"
)

func punchZeroHoles(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func cloneDiskBackup(ctx context.Context, src, dst string) error {
	_ = os.Remove(dst)
	return copySingleSparseFile(ctx, src, dst)
}
