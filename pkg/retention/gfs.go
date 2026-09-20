package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
)

const gcCompactionDeadRatioThreshold = 0.40

// GCRepackJournalMetaKey is the meta key holding an in-progress repack's durability journal.
const GCRepackJournalMetaKey = "operation.gc-repack.v1"

// GCRepackJournal records a repack in flight so an interrupted repack can be rolled back on reopen.
type GCRepackJournal struct {
	Version   int      `json:"version"`
	OldPackID [32]byte `json:"old_pack_id"`
	NewPackID [32]byte `json:"new_pack_id"`
}

// ChunkCatalog is the mutable chunk index used by garbage collection.
type ChunkCatalog interface {
	UpsertChunkLocation(storageID [32]byte, packID [32]byte, offset uint64, length uint32, uploadTimeUnix int64) error
	DeleteChunk(storageID [32]byte) error
	PutMeta(key string, value []byte) error
	GetMeta(key string) (value []byte, found bool, err error)
	DeleteMeta(key string) error
}

// RecoverInterruptedRepack rolls back an incomplete repack detected via its durability journal.
// The old pack and its catalog entries are left untouched so a subsequent GC run retries the repack
// from scratch; only the possibly-orphaned new pack is removed.
func RecoverInterruptedRepack(ctx context.Context, storage pack.StorageEngine, catalog ChunkCatalog) error {
	raw, found, err := catalog.GetMeta(GCRepackJournalMetaKey)
	if err != nil || !found {
		return err
	}
	var journal GCRepackJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return fmt.Errorf("decode gc repack recovery journal: %w", err)
	}
	if journal.Version != 1 {
		return fmt.Errorf("unsupported gc repack recovery journal version %d", journal.Version)
	}

	if err := storage.DeletePack(ctx, journal.NewPackID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove orphaned repack pack: %w", err)
	}
	return catalog.DeleteMeta(GCRepackJournalMetaKey)
}

// GFSPolicy defines retention quotas.
type GFSPolicy struct {
	KeepDaily   int // Son (Days)
	KeepWeekly  int // Father (Weeks)
	KeepMonthly int // Grandfather (Months)
	KeepYearly  int // Archive (Years)
}

// ClassifiedManifest tracks an evaluation decision.
type ClassifiedManifest struct {
	Manifest *manifest.SnapshotManifest
	Keep     bool
	Reason   string
}

// EvaluateGFS classifies manifests according to grandfather-father-son rules.
func EvaluateGFS(manifests []*manifest.SnapshotManifest, policy GFSPolicy) []ClassifiedManifest {
	// Sort newest first
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].Timestamp.After(manifests[j].Timestamp)
	})

	results := make([]ClassifiedManifest, len(manifests))
	dailyBuckets := make(map[string]bool)
	weeklyBuckets := make(map[string]bool)
	monthlyBuckets := make(map[string]bool)
	yearlyBuckets := make(map[string]bool)

	for idx, m := range manifests {
		t := m.Timestamp
		dayKey := t.Format("2006-01-02")
		year, week := t.ISOWeek()
		weekKey := fmt.Sprintf("%d-W%02d", year, week)
		monthKey := t.Format("2006-01")
		yearKey := t.Format("2006")

		keep := false
		var reason string

		// Check Daily
		if len(dailyBuckets) < policy.KeepDaily && !dailyBuckets[dayKey] {
			dailyBuckets[dayKey] = true
			keep = true
			reason = "GFS: Daily (Son)"
		}

		// Check Weekly
		if len(weeklyBuckets) < policy.KeepWeekly && !weeklyBuckets[weekKey] {
			weeklyBuckets[weekKey] = true
			keep = true
			reason = "GFS: Weekly (Father)"
		}

		// Check Monthly
		if len(monthlyBuckets) < policy.KeepMonthly && !monthlyBuckets[monthKey] {
			monthlyBuckets[monthKey] = true
			keep = true
			reason = "GFS: Monthly (Grandfather)"
		}

		// Check Yearly
		if len(yearlyBuckets) < policy.KeepYearly && !yearlyBuckets[yearKey] {
			yearlyBuckets[yearKey] = true
			keep = true
			reason = "GFS: Yearly (Archive)"
		}

		results[idx] = ClassifiedManifest{
			Manifest: m,
			Keep:     keep,
			Reason:   reason,
		}
	}

	return results
}

// GarbageCollector performs two-phase mark-and-sweep across the repository.
type GarbageCollector struct {
	storage    pack.StorageEngine
	catalog    ChunkCatalog
	graceLimit time.Duration
	now        func() time.Time
}

func NewGarbageCollector(storage pack.StorageEngine, catalog ChunkCatalog, graceLimit time.Duration) *GarbageCollector {
	return NewGarbageCollectorWithClock(storage, catalog, graceLimit, time.Now)
}

func NewGarbageCollectorWithClock(
	storage pack.StorageEngine,
	catalog ChunkCatalog,
	graceLimit time.Duration,
	now func() time.Time,
) *GarbageCollector {
	if now == nil {
		now = time.Now
	}

	return &GarbageCollector{
		storage:    storage,
		catalog:    catalog,
		graceLimit: graceLimit,
		now:        now,
	}
}

// ChunkLocation records chunk locations and upload timestamps.
type ChunkLocation struct {
	PackID         [32]byte
	StorageID      [32]byte
	Offset         uint64
	Length         uint32
	UploadTimeUnix int64
}

// Run executes the mark, sweep, and compaction lifecycle.
func (gc *GarbageCollector) Run(
	ctx context.Context,
	retainedManifests []*manifest.SnapshotManifest,
	allKnownChunks []ChunkLocation,
) (purgedCount int, err error) {
	// Phase 1: MARK
	liveSet := make(map[[32]byte]struct{})
	for _, m := range retainedManifests {
		for sid := range m.CollectAllStorageIDs() {
			liveSet[sid] = struct{}{}
		}
	}

	// Phase 2: SWEEP with Epoch Grace Period Guard
	now := gc.now().Unix()
	graceThresholdSec := int64(gc.graceLimit.Seconds())

	deadChunksByPack := make(map[[32]byte][]ChunkLocation)
	allChunksByPack := make(map[[32]byte][]ChunkLocation)
	totalBytesByPack := make(map[[32]byte]uint64)
	deadBytesByPack := make(map[[32]byte]uint64)

	for _, chunk := range allKnownChunks {
		allChunksByPack[chunk.PackID] = append(allChunksByPack[chunk.PackID], chunk)
		totalBytesByPack[chunk.PackID] += uint64(chunk.Length)

		if _, active := liveSet[chunk.StorageID]; !active {
			// In-flight grace period verification:
			// If chunk was uploaded recently, preserve it against race conditions.
			if (now - chunk.UploadTimeUnix) < graceThresholdSec {
				continue
			}

			deadChunksByPack[chunk.PackID] = append(deadChunksByPack[chunk.PackID], chunk)
			deadBytesByPack[chunk.PackID] += uint64(chunk.Length)
			purgedCount++
		}
	}

	// Phase 3: COMPACTION (Repack packfiles with > 40% dead space)
	for packID, totalBytes := range totalBytesByPack {
		deadBytes := deadBytesByPack[packID]
		if totalBytes > 0 && float64(deadBytes)/float64(totalBytes) > gcCompactionDeadRatioThreshold {
			if err := gc.repack(ctx, packID, liveSet, allChunksByPack[packID]); err != nil {
				return purgedCount, fmt.Errorf("failed to repack %x: %w", packID, err)
			}
			continue
		}

		for _, dead := range deadChunksByPack[packID] {
			if gc.catalog != nil {
				if err := gc.catalog.DeleteChunk(dead.StorageID); err != nil {
					return purgedCount, fmt.Errorf("delete dead chunk mapping: %w", err)
				}
			}
		}
	}

	return purgedCount, nil
}

func (gc *GarbageCollector) repack(
	ctx context.Context,
	packID [32]byte,
	liveSet map[[32]byte]struct{},
	packChunks []ChunkLocation,
) error {
	if gc.storage == nil {
		return fmt.Errorf("storage engine is nil")
	}

	if gc.catalog != nil {
		if err := gc.catalog.DeleteMeta(GCRepackJournalMetaKey); err != nil {
			return fmt.Errorf("clear stale repack journal: %w", err)
		}
	}

	builder := pack.NewPackfileBuilder(16 * 1024 * 1024)
	liveOrder := make([]ChunkLocation, 0, len(packChunks))

	for _, chunk := range packChunks {
		if _, keep := liveSet[chunk.StorageID]; !keep {
			if gc.catalog != nil {
				if err := gc.catalog.DeleteChunk(chunk.StorageID); err != nil {
					return fmt.Errorf("delete dead chunk in repack: %w", err)
				}
			}
			continue
		}

		recordSize := pack.RecordSizeFromCipherLen(chunk.Length)
		rawRecord, err := gc.storage.GetChunkRange(ctx, packID, int64(chunk.Offset), recordSize)
		if err != nil {
			return err
		}

		recordSID, nonce, ciphertext, err := pack.DecodeChunkRecord(rawRecord)
		if err != nil {
			return err
		}
		if recordSID != chunk.StorageID {
			return fmt.Errorf("chunk storage id mismatch during repack")
		}

		if err := builder.Append(recordSID, nonce, ciphertext); err != nil {
			return err
		}
		liveOrder = append(liveOrder, chunk)
	}

	if len(liveOrder) == 0 {
		return gc.storage.DeletePack(ctx, packID)
	}

	newPackBytes, newPackID, entries, err := builder.Finalize()
	if err != nil {
		return err
	}
	if len(entries) != len(liveOrder) {
		return fmt.Errorf("live order and index entry count mismatch")
	}

	if gc.catalog != nil {
		journal := GCRepackJournal{Version: 1, OldPackID: packID, NewPackID: newPackID}
		raw, err := json.Marshal(journal)
		if err != nil {
			return fmt.Errorf("encode repack journal: %w", err)
		}
		if err := gc.catalog.PutMeta(GCRepackJournalMetaKey, raw); err != nil {
			return fmt.Errorf("persist repack journal: %w", err)
		}
	}

	if err := gc.storage.PutPack(ctx, newPackID, bytes.NewReader(newPackBytes), int64(len(newPackBytes))); err != nil {
		return err
	}

	now := gc.now().Unix()
	if gc.catalog != nil {
		for i, chunk := range liveOrder {
			entry := entries[i]
			if err := gc.catalog.UpsertChunkLocation(chunk.StorageID, newPackID, entry.Offset, entry.Length, now); err != nil {
				return fmt.Errorf("update chunk location in repack: %w", err)
			}
		}
	}

	if err := gc.storage.DeletePack(ctx, packID); err != nil {
		return err
	}
	if gc.catalog != nil {
		if err := gc.catalog.DeleteMeta(GCRepackJournalMetaKey); err != nil {
			return fmt.Errorf("clear repack journal: %w", err)
		}
	}
	return nil
}
