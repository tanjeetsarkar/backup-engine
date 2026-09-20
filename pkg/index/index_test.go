package index

import (
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestCIDRoundTrip(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	cid := filledArray(0x01)
	storageID := filledArray(0xA1)

	if err := idx.PutCID(cid, storageID); err != nil {
		t.Fatalf("PutCID: %v", err)
	}

	got, found, err := idx.GetStorageID(cid)
	if err != nil {
		t.Fatalf("GetStorageID: %v", err)
	}
	if !found {
		t.Fatalf("expected CID mapping to exist")
	}
	if got != storageID {
		t.Fatalf("storage id mismatch")
	}

	revCID, found, err := idx.GetCID(storageID)
	if err != nil {
		t.Fatalf("GetCID: %v", err)
	}
	if !found {
		t.Fatalf("expected reverse cid mapping to exist")
	}
	if revCID != cid {
		t.Fatalf("cid mismatch in reverse lookup")
	}
}

func TestPutChunkMappingsWritesCompleteRecords(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()
	cid := filledArray(0x31)
	storageID := filledArray(0x41)
	location := ChunkLocation{PackID: filledArray(0x51), Offset: 12, Length: 34, UploadTimeUnix: 56}
	if err := idx.PutChunkMappings([]ChunkMapping{{CID: cid, StorageID: storageID, Location: location}}); err != nil {
		t.Fatal(err)
	}
	gotSID, found, err := idx.GetStorageID(cid)
	if err != nil || !found || gotSID != storageID {
		t.Fatalf("CID mapping: sid=%x found=%v err=%v", gotSID, found, err)
	}
	gotLocation, found, err := idx.GetChunkLocation(storageID)
	if err != nil || !found || gotLocation != location {
		t.Fatalf("chunk location: location=%+v found=%v err=%v", gotLocation, found, err)
	}
}

func TestGetStorageIDNotFound(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	_, found, err := idx.GetStorageID(filledArray(0x0F))
	if err != nil {
		t.Fatalf("GetStorageID: %v", err)
	}
	if found {
		t.Fatalf("expected not found")
	}
}

func TestChunkLocationRoundTripAndPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")

	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	storageID := filledArray(0x33)
	want := ChunkLocation{
		PackID:         filledArray(0x55),
		Offset:         12345,
		Length:         777,
		UploadTimeUnix: 1700000000,
	}
	if err := idx.PutChunkLocation(storageID, want); err != nil {
		t.Fatalf("PutChunkLocation: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx, err = Open(dbPath)
	if err != nil {
		t.Fatalf("Open reopen: %v", err)
	}
	defer idx.Close()

	got, found, err := idx.GetChunkLocation(storageID)
	if err != nil {
		t.Fatalf("GetChunkLocation: %v", err)
	}
	if !found {
		t.Fatalf("expected chunk location to exist")
	}
	if got != want {
		t.Fatalf("chunk location mismatch")
	}
}

func TestSnapshotRoundTripCopiesData(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	snapshotID := filledArray(0x71)
	payload := []byte("encrypted-envelope")
	if err := idx.PutSnapshot(snapshotID, payload); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}

	payload[0] ^= 0xFF

	got, found, err := idx.GetSnapshot(snapshotID)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if !found {
		t.Fatalf("expected snapshot to exist")
	}
	if string(got) != "encrypted-envelope" {
		t.Fatalf("snapshot payload mismatch")
	}
}

func TestDetectCorruptChunkLocationRecord(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer idx.Close()

	storageID := filledArray(0x44)

	if err := idx.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		return b.Put(storageID[:], []byte{0x01, 0x02})
	}); err != nil {
		t.Fatalf("inject corrupt record: %v", err)
	}

	_, _, err = idx.GetChunkLocation(storageID)
	if err != ErrCorruptChunkLocation {
		t.Fatalf("expected ErrCorruptChunkLocation, got %v", err)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return idx
}

func filledArray(fill byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = fill
	}
	return out
}

func TestListSnapshotIDs(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	idA := filledArray(0xAA)
	idB := filledArray(0xBB)

	if err := idx.PutSnapshot(idA, []byte("a")); err != nil {
		t.Fatalf("PutSnapshot A: %v", err)
	}
	if err := idx.PutSnapshot(idB, []byte("b")); err != nil {
		t.Fatalf("PutSnapshot B: %v", err)
	}

	ids, err := idx.ListSnapshotIDs()
	if err != nil {
		t.Fatalf("ListSnapshotIDs: %v", err)
	}

	if len(ids) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(ids))
	}
}

func TestListChunkRecords(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	storageID := filledArray(0x09)
	loc := ChunkLocation{
		PackID:         filledArray(0x22),
		Offset:         42,
		Length:         100,
		UploadTimeUnix: 1700000001,
	}

	if err := idx.PutChunkLocation(storageID, loc); err != nil {
		t.Fatalf("PutChunkLocation: %v", err)
	}

	records, err := idx.ListChunkRecords()
	if err != nil {
		t.Fatalf("ListChunkRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 chunk record, got %d", len(records))
	}

	if records[0].StorageID != storageID {
		t.Fatalf("storage id mismatch")
	}
	if records[0].Location != loc {
		t.Fatalf("chunk location mismatch")
	}
}

func TestDeleteChunkRemovesAllMappings(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	cid := filledArray(0x11)
	storageID := filledArray(0x22)

	if err := idx.PutCID(cid, storageID); err != nil {
		t.Fatalf("PutCID: %v", err)
	}
	if err := idx.PutChunkLocation(storageID, ChunkLocation{PackID: filledArray(0x33), Offset: 1, Length: 2, UploadTimeUnix: 3}); err != nil {
		t.Fatalf("PutChunkLocation: %v", err)
	}

	if err := idx.DeleteChunk(storageID); err != nil {
		t.Fatalf("DeleteChunk: %v", err)
	}

	_, found, err := idx.GetStorageID(cid)
	if err != nil {
		t.Fatalf("GetStorageID: %v", err)
	}
	if found {
		t.Fatalf("expected CID mapping to be removed")
	}

	_, found, err = idx.GetCID(storageID)
	if err != nil {
		t.Fatalf("GetCID: %v", err)
	}
	if found {
		t.Fatalf("expected reverse CID mapping to be removed")
	}

	_, found, err = idx.GetChunkLocation(storageID)
	if err != nil {
		t.Fatalf("GetChunkLocation: %v", err)
	}
	if found {
		t.Fatalf("expected chunk location to be removed")
	}
}

func TestMetaRoundTrip(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	key := "key-check"
	value := []byte("encrypted-marker")

	if err := idx.PutMeta(key, value); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}

	value[0] ^= 0xFF

	got, found, err := idx.GetMeta(key)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if !found {
		t.Fatalf("expected meta key to exist")
	}
	if string(got) != "encrypted-marker" {
		t.Fatalf("unexpected meta value: %q", string(got))
	}
}
