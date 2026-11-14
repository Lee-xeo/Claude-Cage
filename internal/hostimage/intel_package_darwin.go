package hostimage

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

func verifyApplePackageSignature(ctx context.Context, path string) error {
	data, err := exec.CommandContext(ctx, "/usr/sbin/pkgutil", "--check-signature", path).Output()
	if err != nil {
		return errors.New("Apple 安装包签名验证失败")
	}
	text := string(data)
	for _, identity := range []string{"Status: signed Apple Software", "Software Update", "Apple Software Update Certification Authority", "Apple Root CA"} {
		if !strings.Contains(text, identity) {
			return errors.New("安装材料没有 Apple 系统更新签名链")
		}
	}
	return nil
}
