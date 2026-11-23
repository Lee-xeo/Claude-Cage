package cage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ResourceAllocation is separate from network configuration: changing a VM's
// capacity does not change its network authorization fingerprint.
type ResourceAllocation struct {
	Automatic bool `json:"automatic"`
	MemoryGiB int  `json:"memory_gib"`
	CPUs      int  `json:"cpus"`
	DiskGiB   int  `json:"disk_gib,omitempty"`
}

const (
	DefaultDiskGiB          = 24
	MinDiskGiB              = 16
	MaxDiskCapGiB           = 512
	DiskShrinkHeadroomBytes = 4 << 30
)

func ResourceAllocationPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "resource-limits.json")
}

// AutomaticMemoryCapGiB bounds the memory an automatically sized VM may grow
// to. The host share (half of host memory by default) stays the ceiling for a
// manually chosen allocation, so a 64 GiB host no longer starts every VM with
// a 32 GiB budget the guest rarely needs.
const AutomaticMemoryCapGiB = 8

// AutomaticMemoryGiB is the memory ceiling of an automatic allocation on a
// host whose share is shareGiB.
func AutomaticMemoryGiB(shareGiB int) int {
	return min(shareGiB, AutomaticMemoryCapGiB)
}

func (a ResourceAllocation) Limits() (memory, cpus int, err error) {
	memory, cpus = ResourceLimits()
	if a.Automatic {
		return AutomaticMemoryGiB(memory), cpus, nil
	}
	if a.MemoryGiB < 2 || a.MemoryGiB > memory || a.CPUs < 1 {
		return 0, 0, fmt.Errorf("内存上限须为 2–%d GiB，CPU 须为 1–%d 个逻辑核心", memory, cpus)
	}
	// A value saved before this host's ceiling was lowered still starts the
	// environment, at the ceiling.
	return a.MemoryGiB, min(a.CPUs, cpus), nil
}

func LoadResourceAllocation(configPath string) (ResourceAllocation, error) {
	a := ResourceAllocation{Automatic: true}
	b, err := os.ReadFile(ResourceAllocationPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	return a, nil
}

func ConfiguredResourceLimits(configPath string) (int, int, error) {
	a, err := LoadResourceAllocation(configPath)
	if err != nil {
		return 0, 0, err
	}
	return a.Limits()
}

// DiskBounds returns the safe [minGiB, maxGiB] range for manual disk sizing.
// minGiB accounts for observed guest usage plus headroom so a shrink never
// starves the guest filesystem; maxGiB is bounded by the host volume capacity.
func DiskBounds(storageDir string, currentDiskBytes, guestUsedBytes uint64) (minGiB, maxGiB int) {
	minGiB = MinDiskGiB
	if guestUsedBytes > 0 {
		need := int((guestUsedBytes + DiskShrinkHeadroomBytes + (1 << 30) - 1) >> 30)
		if need > minGiB {
			minGiB = need
		}
	}
	maxGiB = 256
	if total, err := DiskVolumeTotalBytes(storageDir); err == nil && total > 0 {
		volGiB := int(total >> 30)
		maxGiB = min(MaxDiskCapGiB, max(64, volGiB))
	}
	currentGiB := int((currentDiskBytes + (1 << 30) - 1) >> 30)
	if currentGiB > maxGiB {
		maxGiB = currentGiB
	}
	if minGiB > maxGiB {
		maxGiB = minGiB
	}
	return minGiB, maxGiB
}

// DiskLimit resolves the effective disk ceiling in GiB for this allocation.
func (a ResourceAllocation) DiskLimit(storageDir string, currentDiskBytes, guestUsedBytes uint64) (int, error) {
	minGiB, maxGiB := DiskBounds(storageDir, currentDiskBytes, guestUsedBytes)
	currentGiB := int((currentDiskBytes + (1 << 30) - 1) >> 30)
	if a.Automatic || a.DiskGiB == 0 {
		if currentGiB > 0 {
			return currentGiB, nil
		}
		return DefaultDiskGiB, nil
	}
	// Shrinking is not offered: see DiskShrinkUnavailable. A smaller saved
	// value keeps the disk as it is, so the environment still starts and the
	// other limits can still be changed however full the disk is.
	if currentGiB > 0 && a.DiskGiB <= currentGiB {
		return currentGiB, nil
	}
	if a.DiskGiB < minGiB || a.DiskGiB > maxGiB {
		return 0, fmt.Errorf("硬盘上限须为 %d–%d GiB", minGiB, maxGiB)
	}
	return a.DiskGiB, nil
}

// DiskShrinkUnavailable is the answer to a request for a smaller disk. The
// offline shrink cannot complete on the supported guest (its early-boot
// mount needs a filesystem type, and each boot grows the root partition
// back), and its rollback could replace the disk of a running machine.
var DiskShrinkUnavailable = errors.New("暂不支持缩小硬盘：当前版本只能扩大硬盘上限，已有数据和硬盘大小保持不变")

// ConfiguredDiskLimit returns the configured disk ceiling for new environment
// setup (defaulting to DefaultDiskGiB when automatic or unset).
func ConfiguredDiskLimit(configPath string) (int, error) {
	a, err := LoadResourceAllocation(configPath)
	if err != nil {
		return 0, err
	}
	return a.DiskLimit(filepath.Dir(configPath), 0, 0)
}
