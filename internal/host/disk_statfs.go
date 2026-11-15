//go:build !windows && (!darwin || !cgo)

package host

func freeGiB(dir string) (int, bool) {
	return statfsFreeGiB(dir)
}
