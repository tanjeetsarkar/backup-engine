package pack

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/zeebo/blake3"
)

const tailEntrySize = 32 + 8 + 4

// ParseTailIndex validates a packfile's header, trailer checksum, and tail index, returning the
// chunk entries it records without decrypting any chunk payload. Disaster-recovery and scrub
// tooling use this to rebuild or verify chunk-location metadata directly from remote pack bytes.
func ParseTailIndex(data []byte) ([]ChunkEntry, error) {
	if len(data) < 8+trailerSize {
		return nil, ErrInvalidPackfile
	}
	if !bytes.Equal(data[0:4], PackfileMagic[:]) {
		return nil, ErrInvalidPackfile
	}
	if version := binary.BigEndian.Uint16(data[4:6]); version != CurrentVersion {
		return nil, fmt.Errorf("unsupported packfile version %d", version)
	}
	count := int(binary.BigEndian.Uint16(data[6:8]))

	trailerStart := len(data) - trailerSize
	body := data[:trailerStart]
	tailOffset := binary.BigEndian.Uint64(data[trailerStart : trailerStart+8])
	wantChecksum := data[trailerStart+8 : trailerStart+trailerSize]

	gotChecksum := blake3.Sum256(body)
	if !bytes.Equal(gotChecksum[:], wantChecksum) {
		return nil, fmt.Errorf("packfile trailer checksum mismatch: pack is corrupt")
	}

	if tailOffset > uint64(len(body)) {
		return nil, fmt.Errorf("packfile tail index offset out of range")
	}
	tail := body[tailOffset:]
	if len(tail) != count*tailEntrySize {
		return nil, fmt.Errorf("packfile tail index size mismatch: expected %d entries (%d bytes), got %d bytes", count, count*tailEntrySize, len(tail))
	}

	entries := make([]ChunkEntry, 0, count)
	for i := 0; i < count; i++ {
		rec := tail[i*tailEntrySize : (i+1)*tailEntrySize]
		var entry ChunkEntry
		copy(entry.StorageID[:], rec[0:32])
		entry.Offset = binary.BigEndian.Uint64(rec[32:40])
		entry.Length = binary.BigEndian.Uint32(rec[40:44])
		entries = append(entries, entry)
	}
	return entries, nil
}
