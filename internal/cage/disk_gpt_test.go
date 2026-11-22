package cage

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildTestGPTImage(t *testing.T, path string, totalBytes int64, partitions [][2]uint64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(totalBytes); err != nil {
		t.Fatal(err)
	}
	const numParts = 128
	const partSize = 128
	entries := make([]byte, numParts*partSize)
	for i, p := range partitions {
		off := i * partSize
		// Non-zero Linux filesystem type GUID.
		copy(entries[off:off+16], []byte{0xaf, 0x3d, 0xc6, 0x0f, 0x83, 0x84, 0x72, 0x47, 0x8e, 0x79, 0x3d, 0x69, 0xd8, 0x47, 0x7d, 0xe4})
		binary.LittleEndian.PutUint64(entries[off+32:off+40], p[0])
		binary.LittleEndian.PutUint64(entries[off+40:off+48], p[1])
	}
	entriesCRC := crc32.ChecksumIEEE(entries)
	totalSectors := uint64(totalBytes / gptSectorSize)
	tableSectors := uint64(len(entries) / gptSectorSize)
	backupHdrLBA := totalSectors - 1
	backupEntriesLBA := backupHdrLBA - tableSectors
	lastUsable := backupEntriesLBA - 1

	hdr := make([]byte, gptSectorSize)
	copy(hdr[0:8], gptSignature)
	binary.LittleEndian.PutUint32(hdr[8:12], 0x00010000)
	binary.LittleEndian.PutUint32(hdr[12:16], 92)
	binary.LittleEndian.PutUint64(hdr[24:32], 1)
	binary.LittleEndian.PutUint64(hdr[32:40], backupHdrLBA)
	binary.LittleEndian.PutUint64(hdr[40:48], 2048)
	binary.LittleEndian.PutUint64(hdr[48:56], lastUsable)
	binary.LittleEndian.PutUint64(hdr[72:80], 2)
	binary.LittleEndian.PutUint32(hdr[80:84], numParts)
	binary.LittleEndian.PutUint32(hdr[84:88], partSize)
	binary.LittleEndian.PutUint32(hdr[88:92], entriesCRC)
	binary.LittleEndian.PutUint32(hdr[16:20], crc32.ChecksumIEEE(hdr[:92]))

	backup := make([]byte, gptSectorSize)
	copy(backup, hdr)
	binary.LittleEndian.PutUint64(backup[24:32], backupHdrLBA)
	binary.LittleEndian.PutUint64(backup[32:40], 1)
	binary.LittleEndian.PutUint64(backup[72:80], backupEntriesLBA)
	binary.LittleEndian.PutUint32(backup[16:20], 0)
	binary.LittleEndian.PutUint32(backup[16:20], crc32.ChecksumIEEE(backup[:92]))

	if _, err := f.WriteAt(hdr, gptSectorSize); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(entries, 2*gptSectorSize); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(entries, int64(backupEntriesLBA)*gptSectorSize); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(backup, int64(backupHdrLBA)*gptSectorSize); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAndRelocateGPTRelocatesValidBackupAndRejectsOverflow(t *testing.T) {
	dir := t.TempDir()
	diskPath := filepath.Join(dir, "disk")
	const origBytes = 20 << 30
	const targetBytes = 16 << 30
	// EFI (2048..262143) + shrunk root ending safely inside 16 GiB.
	safeEnd := uint64(targetBytes/512) - 4096
	buildTestGPTImage(t, diskPath, origBytes, [][2]uint64{
		{2048, 262143},
		{262144, safeEnd},
	})

	f, err := os.OpenFile(diskPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Attempting to shrink below safeEnd must be rejected without modifying primary GPT.
	beforeHdr := make([]byte, gptSectorSize)
	if _, err := f.ReadAt(beforeHdr, gptSectorSize); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAndRelocateGPT(f, 15<<30); err == nil {
		t.Fatal("expected overflow rejection")
	}
	afterRejectHdr := make([]byte, gptSectorSize)
	if _, err := f.ReadAt(afterRejectHdr, gptSectorSize); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeHdr, afterRejectHdr) {
		t.Fatal("primary GPT was modified despite overflow error")
	}

	// Valid target must succeed and produce verifiable Primary + Backup GPT at targetBytes.
	if err := VerifyAndRelocateGPT(f, targetBytes); err != nil {
		t.Fatalf("VerifyAndRelocateGPT failed: %v", err)
	}
	if err := f.Truncate(targetBytes); err != nil {
		t.Fatal(err)
	}
	h, entries, err := readPrimaryGPT(f)
	if err != nil {
		t.Fatalf("primary GPT invalid after relocation: %v", err)
	}
	wantBackupLBA := uint64(targetBytes/512) - 1
	if h.backupLBA != wantBackupLBA || h.lastUsableLBA != wantBackupLBA-33 {
		t.Fatalf("unexpected primary bounds: backup=%d lastUsable=%d wantBackup=%d", h.backupLBA, h.lastUsableLBA, wantBackupLBA)
	}
	backupHdr := make([]byte, gptSectorSize)
	if _, err := f.ReadAt(backupHdr, int64(wantBackupLBA)*gptSectorSize); err != nil {
		t.Fatal(err)
	}
	if string(backupHdr[0:8]) != gptSignature {
		t.Fatal("missing backup GPT signature")
	}
	gotBackupCRC := binary.LittleEndian.Uint32(backupHdr[16:20])
	check := make([]byte, h.headerSize)
	copy(check, backupHdr[:h.headerSize])
	binary.LittleEndian.PutUint32(check[16:20], 0)
	if crc32.ChecksumIEEE(check) != gotBackupCRC {
		t.Fatal("backup GPT header CRC32 mismatch")
	}
	backupEntriesLBA := binary.LittleEndian.Uint64(backupHdr[72:80])
	backupEntries := make([]byte, len(entries))
	if _, err := f.ReadAt(backupEntries, int64(backupEntriesLBA)*gptSectorSize); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(entries, backupEntries) {
		t.Fatal("backup GPT partition entries differ from primary")
	}
}

func TestShrinkColdDiskImageRawCommitAndRollback(t *testing.T) {
	dir := t.TempDir()
	diskPath := filepath.Join(dir, "disk")
	yamlPath := filepath.Join(dir, "lima.yaml")
	const origBytes = 20 << 30
	const targetGiB = 16
	safeEnd := uint64((int64(targetGiB)<<30)/512) - 4096
	buildTestGPTImage(t, diskPath, origBytes, [][2]uint64{
		{2048, 262143},
		{262144, safeEnd},
	})
	if err := os.WriteFile(yamlPath, []byte("cpus: 2\nmemory: \"4GiB\"\ndisk: \"20GiB\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ShrinkColdDiskImage(context.Background(), dir, targetGiB); err != nil {
		t.Fatalf("ShrinkColdDiskImage failed: %v", err)
	}
	info, err := DiskImageUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.VirtualBytes != int64(targetGiB)<<30 {
		t.Fatalf("virtual bytes = %d, want %d", info.VirtualBytes, int64(targetGiB)<<30)
	}
	y, err := os.ReadFile(yamlPath)
	if err != nil || !strings.Contains(string(y), `disk: "16GiB"`) {
		t.Fatalf("lima.yaml not updated: %q %v", string(y), err)
	}

	// Verify rollback restores original size and lima.yaml.
	if err := RollbackDiskShrink(dir); err != nil {
		t.Fatalf("RollbackDiskShrink failed: %v", err)
	}
	restored, err := DiskImageUsage(dir)
	if err != nil || restored.VirtualBytes != origBytes {
		t.Fatalf("rollback virtual bytes = %d, want %d (err=%v)", restored.VirtualBytes, origBytes, err)
	}
	y, _ = os.ReadFile(yamlPath)
	if !strings.Contains(string(y), `disk: "20GiB"`) {
		t.Fatalf("lima.yaml not restored: %q", string(y))
	}

	// Shrink again and verify CommitDiskShrink removes backups.
	if err := ShrinkColdDiskImage(context.Background(), dir, targetGiB); err != nil {
		t.Fatal(err)
	}
	CommitDiskShrink(dir)
	for _, p := range []string{diskPath + diskBackupSuffix, yamlPath + limaYAMLBackupSuffix} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("backup left behind after commit: %s", p)
		}
	}
}

func TestPunchZeroHolesReclaimsAllocatedSpace(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("sparse punch-hole test applies to darwin/linux")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.img")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write 1 MiB header + 8 MiB explicit zeros + 1 MiB trailer.
	marker := bytes.Repeat([]byte{0x5a}, 1<<20)
	zeros := make([]byte, 8<<20)
	if _, err := f.Write(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(zeros); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(marker); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	punched, err := punchZeroHoles(context.Background(), path)
	if err != nil {
		t.Fatalf("punchZeroHoles error: %v", err)
	}
	if punched < 8<<20 {
		t.Fatalf("expected at least 8 MiB punched, got %d", punched)
	}
	// Verify head and tail markers are completely intact.
	out, err := os.ReadFile(path)
	if err != nil || len(out) != 10<<20 {
		t.Fatalf("file size corrupted: len=%d err=%v", len(out), err)
	}
	if !bytes.Equal(out[:1<<20], marker) || !bytes.Equal(out[9<<20:], marker) {
		t.Fatal("marker data corrupted by punchZeroHoles")
	}
}
