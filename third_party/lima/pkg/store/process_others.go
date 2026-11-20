// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package store

// processExited is only needed on Windows; elsewhere signal 0 answers it.
func processExited(int) bool { return false }
