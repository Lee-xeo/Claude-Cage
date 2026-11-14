//go:build !windows && (!darwin || !amd64)

package host

func ConfigureWindowsRuntime() error { return nil }
