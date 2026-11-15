//go:build darwin && cgo

package host

/*
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
long long cageImportantDiskCapacity(const char *path);
*/
import "C"

import "unsafe"

func freeGiB(dir string) (int, bool) {
	path := C.CString(dir)
	defer C.free(unsafe.Pointer(path))
	// User-requested installations may use space macOS can reclaim from
	// caches and local snapshots. statfs alone excludes that capacity.
	if capacity := int64(C.cageImportantDiskCapacity(path)); capacity >= 0 {
		return int(capacity >> 30), true
	}
	return statfsFreeGiB(dir)
}
