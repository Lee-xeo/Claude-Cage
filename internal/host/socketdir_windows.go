package host

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// PrivateSocketDir returns a private local directory for the profile's
// AF_UNIX control sockets, with room more bytes available for socket names
// below it. With create, it is created when needed and restricted to the
// current user; without, only an existing directory is returned, so lookups
// never leave directories behind.
func PrivateSocketDir(profile string, room int, create bool) (string, error) {
	profile = filepath.Clean(profile)
	sum := sha256.Sum256([]byte(profile))
	home, _ := os.UserHomeDir()
	excluded := []string{os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA"), os.TempDir()}
	ensure := EnsurePrivateDir
	if !create {
		ensure = existingDir
	}
	dir, err := chooseWindowsSocketDir(WindowsSocketDirCandidates(profile, home, hex.EncodeToString(sum[:6])), room, excluded, ensure, shortPathName)
	if err != nil {
		return "", fmt.Errorf("Cage 找不到可用于本机控制通道的私有目录：配置目录与用户目录需位于本地磁盘、路径不超过约 %d 个字符，并在含中文等非英文字符时提供 8.3 短文件名（%w）", windowsSocketPathLimit-room, err)
	}
	return dir, nil
}

func existingDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("not a private directory")
	}
	return nil
}

func shortPathName(path string) (string, error) {
	long, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(long, &buffer[0], uint32(len(buffer)))
	if err != nil {
		return "", err
	}
	if n == 0 || int(n) >= len(buffer) {
		return "", fmt.Errorf("no short name")
	}
	return windows.UTF16ToString(buffer[:n]), nil
}
