// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package store

import "golang.org/x/sys/windows"

// processExited reports whether pid names a process that has already exited.
// Windows keeps an exited process openable while any handle to it remains, so
// a successful os.FindProcess does not mean the process is still running. The
// PID cannot be reused while that handle exists, so the object is the recorded
// process. Any failure to tell is reported as still running.
func processExited(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && event == windows.WAIT_OBJECT_0
}
