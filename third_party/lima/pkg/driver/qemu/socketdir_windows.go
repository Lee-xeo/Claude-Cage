// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package qemu

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// socketDir returns the spelling of instDir used for the AF_UNIX sockets that
// QEMU binds and the driver dials. QEMU cannot bind a socket whose path has
// characters outside ASCII, such as a Windows user name in Chinese; the 8.3
// alias names the same directory in ASCII. When 8.3 names are unavailable or
// still non-ASCII (for example a Chinese junction pointing to an ASCII storage
// volume), fall back to the handle's final local-drive path. Without an ASCII
// spelling the path is returned unchanged and QEMU reports its own error.
func socketDir(instDir string) string {
	if isASCII(instDir) {
		return instDir
	}
	if short := shortASCIIPath(instDir); short != "" {
		return short
	}
	if final := finalLocalPath(instDir); final != "" {
		if isASCII(final) {
			return final
		}
		if short := shortASCIIPath(final); short != "" {
			return short
		}
	}
	return instDir
}

func shortASCIIPath(path string) string {
	long, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(long, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || int(n) >= len(buffer) {
		return ""
	}
	if short := windows.UTF16ToString(buffer[:n]); isASCII(short) {
		return short
	}
	return ""
}

func finalLocalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	name := abs
	switch {
	case strings.HasPrefix(name, `\\?\`), strings.HasPrefix(name, `\\.\`):
	case strings.HasPrefix(name, `\\`):
		return ""
	default:
		name = `\\?\` + name
	}
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}
	h, err := windows.CreateFile(
		ptr,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return ""
		}
		if n >= uint32(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		result := windows.UTF16ToString(buf[:n])
		if strings.HasPrefix(result, `\\?\`) && len(result) >= 7 && result[5] == ':' {
			return result[4:]
		}
		return ""
	}
}
