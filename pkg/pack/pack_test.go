package pack

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/zeebo/blake3"
)

func TestAppendRejectsInvalidNonceSize(t *testing.T) {
	builder := NewPackfileBuilder(1024)
	var storageID [32]byte
	copy(storageID[:], []byte("storage-id"))

	err := builder.Append(storageID, []byte("short"), []byte("ciphertext"))
	if !errors.Is(err, ErrInvalidNonceSize) {
		t.Fatalf("expected ErrInvalidNonceSize, got %v", err)
	}
}

func TestFinalizeWritesSpecAlignedLayout(t *testing.T) {
	builder := NewPackfileBuilder(1024)

	var storageID [32]byte
	for i := 0; i < len(storageID); i++ {
		storageID[i] = byte(i)
	}

	nonce := make([]byte, packNonceSize)
	for i := 0; i < len(nonce); i++ {
		nonce[i] = byte(100 + i)
	}

	ciphertext := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	if err := builder.Append(storageID, nonce, ciphertext); err != nil {
		t.Fatalf("Append: %v", err)
	}

	packBytes, packID, entries, err := builder.Finalize()
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Offset != 8 {
		t.Fatalf("expected first chunk offset 8, got %d", entries[0].Offset)
	}
	if entries[0].Length != uint32(len(ciphertext)) {
		t.Fatalf("expected entry length %d, got %d", len(ciphertext), entries[0].Length)
	}

	if !bytes.Equal(packBytes[0:4], PackfileMagic[:]) {
		t.Fatalf("invalid packfile magic")
	}
	if got := binary.BigEndian.Uint16(packBytes[4:6]); got != CurrentVersion {
		t.Fatalf("expected version %d, got %d", CurrentVersion, got)
	}
	if got := binary.BigEndian.Uint16(packBytes[6:8]); got != 1 {
		t.Fatalf("expected chunk count 1, got %d", got)
	}

	recordStart := 8
	recordSID := packBytes[recordStart : recordStart+32]
	if !bytes.Equal(recordSID, storageID[:]) {
		t.Fatalf("record storage id mismatch")
	}
	recordNonce := packBytes[recordStart+32 : recordStart+32+packNonceSize]
	if !bytes.Equal(recordNonce, nonce) {
		t.Fatalf("record nonce mismatch")
	}
	lenPos := recordStart + 32 + packNonceSize
	if got := binary.BigEndian.Uint32(packBytes[lenPos : lenPos+4]); got != uint32(len(ciphertext)) {
		t.Fatalf("expected ciphertext length %d, got %d", len(ciphertext), got)
	}
	recordCipher := packBytes[lenPos+4 : lenPos+4+len(ciphertext)]
	if !bytes.Equal(recordCipher, ciphertext) {
		t.Fatalf("record ciphertext mismatch")
	}

	trailerStart := len(packBytes) - 40
	tailOffset := binary.BigEndian.Uint64(packBytes[trailerStart : trailerStart+8])
	indexStart := int(tailOffset)
	if indexStart != lenPos+4+len(ciphertext) {
		t.Fatalf("unexpected tail offset: got %d, want %d", indexStart, lenPos+4+len(ciphertext))
	}

	tailSID := packBytes[indexStart : indexStart+32]
	if !bytes.Equal(tailSID, storageID[:]) {
		t.Fatalf("tail index storage id mismatch")
	}
	tailEntryOffset := binary.BigEndian.Uint64(packBytes[indexStart+32 : indexStart+40])
	if tailEntryOffset != entries[0].Offset {
		t.Fatalf("tail index offset mismatch")
	}
	tailEntryLen := binary.BigEndian.Uint32(packBytes[indexStart+40 : indexStart+44])
	if tailEntryLen != entries[0].Length {
		t.Fatalf("tail index length mismatch")
	}

	computedChecksum := blake3.Sum256(packBytes[:trailerStart])
	trailerChecksum := packBytes[trailerStart+8 : trailerStart+40]
	if !bytes.Equal(computedChecksum[:], trailerChecksum) {
		t.Fatalf("trailer checksum mismatch")
	}

	expectedPackID := blake3.Sum256(packBytes)
	if expectedPackID != packID {
		t.Fatalf("pack id mismatch")
	}
}
