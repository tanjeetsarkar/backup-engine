package pack

import (
	"crypto/rand"
	"testing"
)

func TestParseTailIndexRoundTrip(t *testing.T) {
	builder := NewPackfileBuilder(1024)
	var storageIDs [][32]byte
	for i := 0; i < 3; i++ {
		var storageID [32]byte
		storageID[0] = byte(i + 1)
		nonce := make([]byte, packNonceSize)
		_, _ = rand.Read(nonce)
		ciphertext := make([]byte, 16+i)
		_, _ = rand.Read(ciphertext)
		if err := builder.Append(storageID, nonce, ciphertext); err != nil {
			t.Fatalf("append: %v", err)
		}
		storageIDs = append(storageIDs, storageID)
	}

	data, _, wantEntries, err := builder.Finalize()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	entries, err := ParseTailIndex(data)
	if err != nil {
		t.Fatalf("ParseTailIndex: %v", err)
	}
	if len(entries) != len(wantEntries) {
		t.Fatalf("got %d entries, want %d", len(entries), len(wantEntries))
	}
	for i, entry := range entries {
		if entry.StorageID != wantEntries[i].StorageID || entry.Offset != wantEntries[i].Offset || entry.Length != wantEntries[i].Length {
			t.Fatalf("entry %d = %+v, want %+v", i, entry, wantEntries[i])
		}
	}
}

func TestParseTailIndexCorruptChecksum(t *testing.T) {
	builder := NewPackfileBuilder(1024)
	var storageID [32]byte
	storageID[0] = 1
	nonce := make([]byte, packNonceSize)
	if err := builder.Append(storageID, nonce, []byte("payload")); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, _, _, err := builder.Finalize()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	data[0] ^= 0xFF // corrupt magic/body, leaving trailer checksum stale
	if _, err := ParseTailIndex(data); err == nil {
		t.Fatalf("expected checksum mismatch error")
	}
}
