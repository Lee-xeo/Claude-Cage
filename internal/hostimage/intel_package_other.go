//go:build !darwin

package hostimage

import (
	"context"
	"errors"
)

func verifyApplePackageSignature(context.Context, string) error {
	return errors.New("Apple 安装包需在 macOS 上验证")
}
