package pack

import "testing"

// FuzzParseTailIndex feeds arbitrary bytes at the boundary that parses packfiles read back from
// storage (local disk or a remote bucket that could be tampered with/corrupted).
func FuzzParseTailIndex(f *testing.F) {
	builder := NewPackfileBuilder(1024)
	var storageID [32]byte
	storageID[0] = 1
	nonce := make([]byte, packNonceSize)
	if err := builder.Append(storageID, nonce, []byte("seed-payload")); err != nil {
		f.Fatalf("seed append: %v", err)
	}
	seed, _, _, err := builder.Finalize()
	if err != nil {
		f.Fatalf("seed finalize: %v", err)
	}
	f.Add(seed)
	f.Add([]byte(""))
	f.Add([]byte("short"))
	f.Add(append([]byte(nil), PackfileMagic[:]...))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must never panic on arbitrary/corrupt input; an error return is fine.
		_, _ = ParseTailIndex(data)
	})
}

// FuzzDecodeChunkRecord feeds arbitrary bytes at the boundary that parses one chunk record read
// from a packfile.
func FuzzDecodeChunkRecord(f *testing.F) {
	var storageID [32]byte
	storageID[0] = 2
	nonce := make([]byte, packNonceSize)
	builder := NewPackfileBuilder(1024)
	if err := builder.Append(storageID, nonce, []byte("payload")); err != nil {
		f.Fatalf("seed append: %v", err)
	}
	data, _, entries, err := builder.Finalize()
	if err != nil {
		f.Fatalf("seed finalize: %v", err)
	}
	record := data[entries[0].Offset : int64(entries[0].Offset)+RecordSizeFromCipherLen(entries[0].Length)]
	f.Add(record)
	f.Add([]byte(""))
	f.Add([]byte("short"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _, _, _ = DecodeChunkRecord(raw)
	})
}
