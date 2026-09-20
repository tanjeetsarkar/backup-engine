package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
	"github.com/tanjeetsarkar/backup-engine/pkg/retention"
)

// HardDeleteSnapshot permanently removes a snapshot immediately, bypassing recoverable trash,
// and reclaims any chunks and packfile space that become unreferenced as a result.
func (e *Engine) HardDeleteSnapshot(ctx context.Context, snapshotID [32]byte) (HardDeleteResult, error) {
	return e.HardDeleteSnapshotDetailed(ctx, snapshotID, nil)
}

// HardDeleteSnapshotDetailed is HardDeleteSnapshot with progress reporting.
func (e *Engine) HardDeleteSnapshotDetailed(ctx context.Context, snapshotID [32]byte, reporter Reporter) (result HardDeleteResult, err error) {
	started := time.Now()
	result.SnapshotID = snapshotID
	defer func() { e.recordTransaction(OperationHardDelete, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()

	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return result, err
	}
	defer lock.release()

	emit(reporter, ProgressEvent{Operation: OperationHardDelete, Phase: PhasePreparing, Level: EventInfo, Message: "Loading snapshot manifests"})

	ids, err := e.idx.ListSnapshotIDs()
	if err != nil {
		return result, err
	}

	var target *manifest.SnapshotManifest
	retained := make([]*manifest.SnapshotManifest, 0, len(ids))
	for _, id := range ids {
		env, found, getErr := e.idx.GetSnapshot(id)
		if getErr != nil {
			return result, getErr
		}
		if !found {
			continue
		}
		snap, decErr := manifest.DecryptSnapshotEnvelope(env, e.km)
		if decErr != nil {
			return result, decErr
		}
		if id == snapshotID {
			target = snap
			continue
		}
		retained = append(retained, snap)
	}
	if target == nil {
		return result, fmt.Errorf("snapshot not found")
	}

	// A snapshot's chunks are only reclaimable if no other snapshot still references them.
	liveSet := make(map[[32]byte]struct{})
	for _, snap := range retained {
		for sid := range snap.CollectAllStorageIDs() {
			liveSet[sid] = struct{}{}
		}
	}
	targetChunks := target.CollectAllStorageIDs()

	records, err := e.idx.ListChunkRecords()
	if err != nil {
		return result, err
	}
	chunks := make([]retention.ChunkLocation, 0, len(records))
	for _, r := range records {
		chunks = append(chunks, retention.ChunkLocation{
			PackID:         r.Location.PackID,
			StorageID:      r.StorageID,
			Offset:         r.Location.Offset,
			Length:         r.Location.Length,
			UploadTimeUnix: r.Location.UploadTimeUnix,
		})
		if _, owned := targetChunks[r.StorageID]; owned {
			if _, stillLive := liveSet[r.StorageID]; !stillLive {
				result.BytesReclaimed += int64(r.Location.Length)
			}
		}
	}

	emit(reporter, ProgressEvent{Operation: OperationHardDelete, Phase: PhaseCompacting, Level: EventInfo, Message: "Reclaiming unreferenced chunks", Total: int64(len(chunks)), Unit: "chunks"})

	// No grace period: the mutation lock guarantees no concurrent backup can race this deletion.
	gc := retention.NewGarbageCollector(e.storage, e.idx, 0)
	purged, err := gc.Run(ctx, retained, chunks)
	if err != nil {
		return result, err
	}
	result.ChunksReclaimed = int64(purged)

	if err := e.idx.DeleteSnapshots([][32]byte{snapshotID}); err != nil {
		return result, fmt.Errorf("delete snapshot metadata: %w", err)
	}

	emit(reporter, ProgressEvent{Operation: OperationHardDelete, Phase: PhaseComplete, Level: EventSuccess, Message: "Snapshot removed instantly", Completed: result.ChunksReclaimed, Unit: "chunks"})
	return result, nil
}
