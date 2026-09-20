package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
)

// fakeStorage is a minimal in-memory pack.StorageEngine for repack/journal tests.
type fakeStorage struct {
	mu    sync.Mutex
	packs map[[32]byte][]byte
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{packs: make(map[[32]byte][]byte)}
}

func (s *fakeStorage) PutPack(_ context.Context, packID [32]byte, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packs[packID] = data
	return nil
}

func (s *fakeStorage) GetPack(_ context.Context, packID [32]byte) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.packs[packID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeStorage) GetChunkRange(_ context.Context, packID [32]byte, offset int64, length int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.packs[packID]
	if !ok {
		return nil, os.ErrNotExist
	}
	if offset < 0 || length < 0 || offset+length > int64(len(data)) {
		return nil, errors.New("out of range")
	}
	out := make([]byte, length)
	copy(out, data[offset:offset+length])
	return out, nil
}

func (s *fakeStorage) DeletePack(_ context.Context, packID [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.packs[packID]; !ok {
		return os.ErrNotExist
	}
	delete(s.packs, packID)
	return nil
}

func (s *fakeStorage) ListPacks(_ context.Context) ([][32]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([][32]byte, 0, len(s.packs))
	for id := range s.packs {
		ids = append(ids, id)
	}
	return ids, nil
}

// fakeCatalog is a minimal in-memory ChunkCatalog for repack/journal tests.
type fakeCatalog struct {
	mu    sync.Mutex
	chunk map[[32]byte]ChunkLocation
	meta  map[string][]byte
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{chunk: make(map[[32]byte]ChunkLocation), meta: make(map[string][]byte)}
}

func (c *fakeCatalog) UpsertChunkLocation(storageID [32]byte, packID [32]byte, offset uint64, length uint32, uploadTimeUnix int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunk[storageID] = ChunkLocation{PackID: packID, StorageID: storageID, Offset: offset, Length: length, UploadTimeUnix: uploadTimeUnix}
	return nil
}

func (c *fakeCatalog) DeleteChunk(storageID [32]byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.chunk, storageID)
	return nil
}

func (c *fakeCatalog) PutMeta(key string, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	payload := make([]byte, len(value))
	copy(payload, value)
	c.meta[key] = payload
	return nil
}

func (c *fakeCatalog) GetMeta(key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, found := c.meta[key]
	return value, found, nil
}

func (c *fakeCatalog) DeleteMeta(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.meta, key)
	return nil
}

func TestEvaluateGFSDailyOnly(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	manifests := make([]*manifest.SnapshotManifest, 0, 5)
	for i := 0; i < 5; i++ {
		manifests = append(manifests, &manifest.SnapshotManifest{
			Timestamp: now.Add(-time.Duration(i) * 24 * time.Hour),
		})
	}

	results := EvaluateGFS(manifests, GFSPolicy{
		KeepDaily:   2,
		KeepWeekly:  0,
		KeepMonthly: 0,
		KeepYearly:  0,
	})

	kept := 0
	for _, r := range results {
		if r.Keep {
			kept++
			if r.Reason != "GFS: Daily (Son)" {
				t.Fatalf("expected daily reason, got %q", r.Reason)
			}
		}
	}

	if kept != 2 {
		t.Fatalf("expected 2 kept manifests, got %d", kept)
	}
}

func TestGarbageCollectorRunGracePeriod(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	gc := NewGarbageCollectorWithClock(nil, nil, 24*time.Hour, func() time.Time { return now })

	var liveSID [32]byte
	liveSID[0] = 1

	retained := []*manifest.SnapshotManifest{
		{
			Root: &manifest.DirectoryNode{
				Path: "/",
				Files: []*manifest.FileNode{
					{StorageIDs: [][32]byte{liveSID}},
				},
			},
		},
	}

	var packID [32]byte
	packID[0] = 9

	var deadOldSID [32]byte
	deadOldSID[0] = 2
	var deadRecentSID [32]byte
	deadRecentSID[0] = 3

	chunks := []ChunkLocation{
		{
			PackID:         packID,
			StorageID:      liveSID,
			Length:         10,
			UploadTimeUnix: now.Add(-48 * time.Hour).Unix(),
		},
		{
			PackID:         packID,
			StorageID:      deadOldSID,
			Length:         10,
			UploadTimeUnix: now.Add(-48 * time.Hour).Unix(),
		},
		{
			PackID:         packID,
			StorageID:      deadRecentSID,
			Length:         10,
			UploadTimeUnix: now.Add(-6 * time.Hour).Unix(),
		},
	}

	purged, err := gc.Run(context.Background(), retained, chunks)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if purged != 1 {
		t.Fatalf("expected 1 purged chunk, got %d", purged)
	}
}

// buildFakePack writes a real packfile with one live and two dead chunks (>40% dead)
// into storage, returning the packID and the chunk locations describing it.
func buildFakePack(t *testing.T, storage *fakeStorage, liveSID [32]byte) ([32]byte, []ChunkLocation) {
	t.Helper()
	builder := pack.NewPackfileBuilder(16 * 1024 * 1024)
	nonce := bytes.Repeat([]byte{0xAB}, 24)

	var deadSID1, deadSID2 [32]byte
	deadSID1[0], deadSID2[0] = 2, 3

	for _, sid := range [][32]byte{liveSID, deadSID1, deadSID2} {
		if err := builder.Append(sid, nonce, bytes.Repeat([]byte{0xCD}, 32)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	data, packID, entries, err := builder.Finalize()
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := storage.PutPack(context.Background(), packID, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("PutPack: %v", err)
	}

	locations := make([]ChunkLocation, 0, len(entries))
	for _, entry := range entries {
		locations = append(locations, ChunkLocation{
			PackID:    packID,
			StorageID: entry.StorageID,
			Offset:    entry.Offset,
			Length:    entry.Length,
		})
	}
	return packID, locations
}

func TestGarbageCollectorRunRepacksAndClearsJournal(t *testing.T) {
	storage := newFakeStorage()
	catalog := newFakeCatalog()

	var liveSID [32]byte
	liveSID[0] = 1
	oldPackID, locations := buildFakePack(t, storage, liveSID)

	retained := []*manifest.SnapshotManifest{
		{Root: &manifest.DirectoryNode{Path: "/", Files: []*manifest.FileNode{{StorageIDs: [][32]byte{liveSID}}}}},
	}

	gc := NewGarbageCollector(storage, catalog, 0)
	purged, err := gc.Run(context.Background(), retained, locations)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if purged != 2 {
		t.Fatalf("expected 2 purged chunks, got %d", purged)
	}

	if _, err := storage.GetPack(context.Background(), oldPackID); err == nil {
		t.Fatalf("expected old pack to be removed after repack")
	}
	if _, found, _ := catalog.GetMeta(GCRepackJournalMetaKey); found {
		t.Fatalf("expected repack journal to be cleared after successful repack")
	}
	if _, ok := catalog.chunk[liveSID]; !ok {
		t.Fatalf("expected live chunk location to survive repack")
	}
}

func TestRecoverInterruptedRepackRemovesOrphanedPack(t *testing.T) {
	storage := newFakeStorage()
	catalog := newFakeCatalog()

	var liveSID [32]byte
	liveSID[0] = 1
	oldPackID, locations := buildFakePack(t, storage, liveSID)
	for _, loc := range locations {
		if err := catalog.UpsertChunkLocation(loc.StorageID, loc.PackID, loc.Offset, loc.Length, 0); err != nil {
			t.Fatalf("UpsertChunkLocation: %v", err)
		}
	}

	// Simulate a crash after the new pack was written but before catalog/old-pack cleanup completed.
	var newPackID [32]byte
	newPackID[0] = 0xFF
	if err := storage.PutPack(context.Background(), newPackID, bytes.NewReader([]byte("orphaned")), 8); err != nil {
		t.Fatalf("PutPack: %v", err)
	}
	journal := GCRepackJournal{Version: 1, OldPackID: oldPackID, NewPackID: newPackID}
	raw, err := json.Marshal(journal)
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	if err := catalog.PutMeta(GCRepackJournalMetaKey, raw); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}

	if err := RecoverInterruptedRepack(context.Background(), storage, catalog); err != nil {
		t.Fatalf("RecoverInterruptedRepack: %v", err)
	}

	if _, err := storage.GetPack(context.Background(), newPackID); err == nil {
		t.Fatalf("expected orphaned new pack to be removed")
	}
	if _, err := storage.GetPack(context.Background(), oldPackID); err != nil {
		t.Fatalf("expected old pack to remain untouched: %v", err)
	}
	if _, found, _ := catalog.GetMeta(GCRepackJournalMetaKey); found {
		t.Fatalf("expected journal to be cleared")
	}
	if _, ok := catalog.chunk[liveSID]; !ok {
		t.Fatalf("expected pre-existing chunk location for live chunk to remain")
	}
}

func TestRecoverInterruptedRepackNoJournalIsNoop(t *testing.T) {
	storage := newFakeStorage()
	catalog := newFakeCatalog()
	if err := RecoverInterruptedRepack(context.Background(), storage, catalog); err != nil {
		t.Fatalf("RecoverInterruptedRepack: %v", err)
	}
}
