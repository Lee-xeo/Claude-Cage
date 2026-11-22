package cage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

const (
	gptSectorSize    = 512
	gptHeaderMinSize = 92
	gptSignature     = "EFI PART"
)

type gptHeader struct {
	raw             []byte
	headerSize      uint32
	currentLBA      uint64
	backupLBA       uint64
	firstUsableLBA  uint64
	lastUsableLBA   uint64
	partEntryLBA    uint64
	numPartEntries  uint32
	partEntrySize   uint32
	partEntriesCRC  uint32
}

func readPrimaryGPT(r io.ReaderAt) (*gptHeader, []byte, error) {
	sector := make([]byte, gptSectorSize)
	if _, err := r.ReadAt(sector, gptSectorSize); err != nil {
		return nil, nil, fmt.Errorf("read primary GPT header: %w", err)
	}
	if string(sector[0:8]) != gptSignature {
		return nil, nil, errors.New("missing primary GPT signature")
	}
	headerSize := binary.LittleEndian.Uint32(sector[12:16])
	if headerSize < gptHeaderMinSize || headerSize > gptSectorSize {
		return nil, nil, fmt.Errorf("invalid GPT header size: %d", headerSize)
	}
	wantCRC := binary.LittleEndian.Uint32(sector[16:20])
	check := make([]byte, headerSize)
	copy(check, sector[:headerSize])
	binary.LittleEndian.PutUint32(check[16:20], 0)
	if got := crc32.ChecksumIEEE(check); got != wantCRC {
		return nil, nil, fmt.Errorf("invalid primary GPT header CRC32: got %#x, want %#x", got, wantCRC)
	}
	h := &gptHeader{
		raw:            sector,
		headerSize:     headerSize,
		currentLBA:     binary.LittleEndian.Uint64(sector[24:32]),
		backupLBA:      binary.LittleEndian.Uint64(sector[32:40]),
		firstUsableLBA: binary.LittleEndian.Uint64(sector[40:48]),
		lastUsableLBA:  binary.LittleEndian.Uint64(sector[48:56]),
		partEntryLBA:   binary.LittleEndian.Uint64(sector[72:80]),
		numPartEntries: binary.LittleEndian.Uint32(sector[80:84]),
		partEntrySize:  binary.LittleEndian.Uint32(sector[84:88]),
		partEntriesCRC: binary.LittleEndian.Uint32(sector[88:92]),
	}
	if h.currentLBA != 1 || h.partEntrySize < 128 || h.partEntrySize > 1024 || h.numPartEntries == 0 || h.numPartEntries > 1024 {
		return nil, nil, errors.New("unsupported GPT layout")
	}
	tableSize := int64(h.numPartEntries) * int64(h.partEntrySize)
	entries := make([]byte, tableSize)
	if _, err := r.ReadAt(entries, int64(h.partEntryLBA)*gptSectorSize); err != nil {
		return nil, nil, fmt.Errorf("read GPT partition entries: %w", err)
	}
	if got := crc32.ChecksumIEEE(entries); got != h.partEntriesCRC {
		return nil, nil, fmt.Errorf("invalid GPT partition array CRC32: got %#x, want %#x", got, h.partEntriesCRC)
	}
	return h, entries, nil
}

// VerifyAndRelocateGPT validates that every non-empty partition fits strictly
// inside targetBytes (leaving room for the backup GPT table and header), then
// updates the primary GPT header and writes a matching backup GPT at the new
// end of a raw disk image before truncation.
func VerifyAndRelocateGPT(f *os.File, targetBytes int64) error {
	if targetBytes < 1<<30 || targetBytes%gptSectorSize != 0 {
		return fmt.Errorf("invalid GPT target size: %d", targetBytes)
	}
	h, entries, err := readPrimaryGPT(f)
	if err != nil {
		return err
	}
	tableSectors := (uint64(len(entries)) + gptSectorSize - 1) / gptSectorSize
	targetSectors := uint64(targetBytes / gptSectorSize)
	if targetSectors <= h.firstUsableLBA+tableSectors+2 {
		return errors.New("target disk size is too small for GPT layout")
	}
	newBackupHeaderLBA := targetSectors - 1
	newBackupEntriesLBA := newBackupHeaderLBA - tableSectors
	newLastUsableLBA := newBackupEntriesLBA - 1

	var zeroType [16]byte
	for i := uint32(0); i < h.numPartEntries; i++ {
		off := int(i * h.partEntrySize)
		entry := entries[off : off+int(h.partEntrySize)]
		if bytes.Equal(entry[0:16], zeroType[:]) {
			continue
		}
		firstLBA := binary.LittleEndian.Uint64(entry[32:40])
		lastLBA := binary.LittleEndian.Uint64(entry[40:48])
		if lastLBA < firstLBA || lastLBA > newLastUsableLBA {
			return fmt.Errorf("partition %d (LBA %d..%d) exceeds target disk usable end %d", i+1, firstLBA, lastLBA, newLastUsableLBA)
		}
	}

	// Update primary header in place.
	primary := make([]byte, gptSectorSize)
	copy(primary, h.raw)
	binary.LittleEndian.PutUint64(primary[24:32], 1)
	binary.LittleEndian.PutUint64(primary[32:40], newBackupHeaderLBA)
	binary.LittleEndian.PutUint64(primary[48:56], newLastUsableLBA)
	binary.LittleEndian.PutUint32(primary[16:20], 0)
	binary.LittleEndian.PutUint32(primary[16:20], crc32.ChecksumIEEE(primary[:h.headerSize]))

	// Build matching backup header for newBackupHeaderLBA.
	backup := make([]byte, gptSectorSize)
	copy(backup, primary)
	binary.LittleEndian.PutUint64(backup[24:32], newBackupHeaderLBA)
	binary.LittleEndian.PutUint64(backup[32:40], 1)
	binary.LittleEndian.PutUint64(backup[72:80], newBackupEntriesLBA)
	binary.LittleEndian.PutUint32(backup[16:20], 0)
	binary.LittleEndian.PutUint32(backup[16:20], crc32.ChecksumIEEE(backup[:h.headerSize]))

	// Pad backup entries to whole sectors.
	paddedEntries := make([]byte, tableSectors*gptSectorSize)
	copy(paddedEntries, entries)

	if _, err := f.WriteAt(paddedEntries, int64(newBackupEntriesLBA)*gptSectorSize); err != nil {
		return fmt.Errorf("write backup GPT entries: %w", err)
	}
	if _, err := f.WriteAt(backup, int64(newBackupHeaderLBA)*gptSectorSize); err != nil {
		return fmt.Errorf("write backup GPT header: %w", err)
	}
	if _, err := f.WriteAt(primary, gptSectorSize); err != nil {
		return fmt.Errorf("write primary GPT header: %w", err)
	}
	return f.Sync()
}
