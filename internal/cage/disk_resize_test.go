package cage

import (
	"strings"
	"testing"
)

func TestPlanDiskShrinkValidatesSafetyAndComputesTargets(t *testing.T) {
	const disk64Sectors = (64 << 30) / 512
	valid := DiskShrinkInspection{
		FSType:      "ext4",
		RootDev:     "/dev/vda1",
		ParentDisk:  "vda",
		PartNum:     1,
		PartStart:   262144,
		PartSectors: disk64Sectors - 262144 - 34,
		DiskSectors: disk64Sectors,
		OtherMaxEnd: 262143,
		UsedBytes:   8 << 30,
		FSBlockSize: 4096,
		FSMinBlocks: (9 << 30) / 4096,
	}

	plan, err := PlanDiskShrink(valid, 24)
	if err != nil {
		t.Fatalf("expected valid shrink plan to 24 GiB, got error: %v", err)
	}
	if plan.TargetDiskBytes != 24<<30 || plan.PartNum != 1 || plan.ParentDisk != "vda" || len(plan.Nonce) != 24 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.TargetPartEndLBA > plan.TargetDiskSectors-34 {
		t.Fatalf("partition end %d collides with backup GPT at %d", plan.TargetPartEndLBA, plan.TargetDiskSectors-34)
	}
	if plan.TargetFSBlocks*valid.FSBlockSize >= plan.TargetPartSectors*512 {
		t.Fatal("filesystem size must remain strictly smaller than target partition size")
	}

	// Reject shrink when used + headroom exceeds target.
	tooTight := valid
	tooTight.UsedBytes = 21 << 30
	if _, err := PlanDiskShrink(tooTight, 24); err == nil || !strings.Contains(err.Error(), "至少需要") {
		t.Fatalf("expected headroom rejection, got: %v", err)
	}

	// Reject non-ext4.
	xfs := valid
	xfs.FSType = "xfs"
	if _, err := PlanDiskShrink(xfs, 24); err == nil {
		t.Fatal("expected non-ext4 rejection")
	}

	// Reject root partition not at end of disk.
	notLast := valid
	notLast.OtherMaxEnd = valid.PartStart + 100
	if _, err := PlanDiskShrink(notLast, 24); err == nil {
		t.Fatal("expected non-tail partition rejection")
	}

	// Reject target >= current.
	if _, err := PlanDiskShrink(valid, 64); err == nil {
		t.Fatal("expected non-shrink rejection")
	}
}

func TestParseDiskShrinkInspectionAndResourceProbeDisk(t *testing.T) {
	raw := `fstype=ext4
rootdev=/dev/vda1
parent=vda
partnum=1
part_start=262144
part_size=50069471
disk_size=50331648
other_max_end=262143
used_bytes=9123456789
block_size=4096
min_blocks=2500000
`
	insp, err := ParseDiskShrinkInspection([]byte(raw))
	if err != nil {
		t.Fatalf("ParseDiskShrinkInspection failed: %v", err)
	}
	if insp.FSType != "ext4" || insp.ParentDisk != "vda" || insp.PartNum != 1 || insp.UsedBytes != 9123456789 || insp.FSMinBlocks != 2500000 {
		t.Fatalf("unexpected inspection: %+v", insp)
	}

	probe := "MemTotal: 4194304\nMemAvailable: 2097152\nSwapTotal: 0\nSwapFree: 0\noom_kill 0\nsome avg10=0.00\ncpu 100 0 50 850 0 0 0 0\ndisk_root 25000000000 9000000000 15000000000\nboot 11111111-2222-3333-4444-555555555555\n"
	sample, err := parseResources([]byte(probe))
	if err != nil {
		t.Fatalf("parseResources failed: %v", err)
	}
	if sample.DiskTotal != 25000000000 || sample.DiskUsed != 9000000000 || sample.DiskAvail != 15000000000 {
		t.Fatalf("unexpected disk metrics: %+v", sample)
	}
}

func TestDiskBoundsAndAllocationLimits(t *testing.T) {
	dir := t.TempDir()
	minGiB, maxGiB := DiskBounds(dir, 24<<30, 15<<30)
	if minGiB != 19 {
		t.Fatalf("minGiB = %d, want 19 (15 GiB used + 4 GiB headroom)", minGiB)
	}
	if maxGiB < 64 {
		t.Fatalf("maxGiB = %d, want >= 64", maxGiB)
	}
	// Automatic keeps existing disk size.
	if got, err := (ResourceAllocation{Automatic: true}).DiskLimit(dir, 40<<30, 10<<30); err != nil || got != 40 {
		t.Fatalf("automatic existing disk = %d, %v; want 40", got, err)
	}
	// A smaller manual value keeps the disk as it is: shrinking is not
	// offered, and a full disk must not block the other limits.
	for _, smaller := range []int{minGiB - 1, 32, 40} {
		if got, err := (ResourceAllocation{Automatic: false, DiskGiB: smaller}).DiskLimit(dir, 40<<30, 39<<30); err != nil || got != 40 {
			t.Fatalf("manual disk %d on a 40 GiB disk = %d, %v; want 40", smaller, got, err)
		}
	}
	// Growing within the range succeeds; a new disk still honors the minimum.
	if got, err := (ResourceAllocation{Automatic: false, DiskGiB: 48}).DiskLimit(dir, 40<<30, 15<<30); err != nil || got != 48 {
		t.Fatalf("manual disk = %d, %v; want 48", got, err)
	}
	if _, err := (ResourceAllocation{Automatic: false, DiskGiB: MinDiskGiB - 1}).DiskLimit(dir, 0, 0); err == nil {
		t.Fatal("expected below-minimum disk error for a new disk")
	}
}
