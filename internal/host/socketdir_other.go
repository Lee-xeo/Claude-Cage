//go:build !windows

package host

import "errors"

// PrivateSocketDir is only used for Windows control sockets.
func PrivateSocketDir(string, int, bool) (string, error) {
	return "", errors.New("private socket directories are chosen only on Windows")
}
