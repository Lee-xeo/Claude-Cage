// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package qemu

import "golang.org/x/sys/windows"

// socketDir returns the spelling of instDir used for the AF_UNIX sockets that
// QEMU binds and the driver dials. QEMU cannot bind a socket whose path has
// characters outside ASCII, such as a Windows user name in Chinese; the 8.3
// alias names the same directory in ASCII. Without an ASCII alias the path is
// returned unchanged and QEMU reports its own error.
func socketDir(instDir string) string {
	if isASCII(instDir) {
		return instDir
	}
	long, err := windows.UTF16PtrFromString(instDir)
	if err != nil {
		return instDir
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(long, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || int(n) >= len(buffer) {
		return instDir
	}
	if short := windows.UTF16ToString(buffer[:n]); isASCII(short) {
		return short
	}
	return instDir
}
