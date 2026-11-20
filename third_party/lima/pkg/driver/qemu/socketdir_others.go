// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package qemu

// socketDir is only needed on Windows, where socket paths must be ASCII.
func socketDir(instDir string) string { return instDir }
