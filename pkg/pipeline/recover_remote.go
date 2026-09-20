package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
)

// RecoverConfig configures rebuilding a repository entirely from its remote replica.
type RecoverConfig struct {
	RepoDir    string
	Passphrase []byte
	Salt       []byte
	// Remote describes the offsite storage previously populated via Engine.Replicate.
	Remote StorageConfig
}

// RecoverFromRemote rebuilds a fresh local repository (snapshots, the chunk-location index, the
// CID/dedup directory, and pack contents) entirely from a remote replica populated by a prior
// Replicate run. RepoDir must be empty or otherwise uninitialized; Open's existing key-check guard
// rejects a repository that already has snapshots or chunk records to avoid clobbering a live
// repository.
//
// Full recovery (including the ability to actually restore/verify content, not just list
// snapshots) depends on the remote having a replicated CID directory: chunk encryption keys are
// derived from the CID (see pkg/crypto.DeriveChunkKey), and StorageID -> CID cannot be recomputed
// from anything stored in a pack, so a repository replicated before this feature existed -- or
// with recovery-affecting replication disabled -- cannot have its chunks decrypted after this kind
// of total loss. Run replicate at least once after upgrading to ensure the CID directory exists
// remotely.
func RecoverFromRemote(ctx context.Context, cfg RecoverConfig) (RecoverResult, error) {
	return RecoverFromRemoteDetailed(ctx, cfg, nil)
}

// RecoverFromRemoteDetailed is RecoverFromRemote with progress reporting.
func RecoverFromRemoteDetailed(ctx context.Context, cfg RecoverConfig, reporter Reporter) (result RecoverResult, err error) {
	started := time.Now()

	if cfg.Remote.Backend == "" || cfg.Remote.Backend == StorageBackendLocal {
		return result, fmt.Errorf("recovery remote backend must not be local")
	}

	emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhasePreparing, Level: EventInfo, Message: "Opening fresh local repository"})
	e, err := Open(EngineConfig{RepoDir: cfg.RepoDir, Passphrase: cfg.Passphrase, Salt: cfg.Salt})
	if err != nil {
		return result, fmt.Errorf("open local repository: %w", err)
	}
	defer e.Close()
	defer func() { e.recordTransaction(OperationRecover, started, err, result) }()

	remoteManifests, err := NewStorageEngine("", withSubPrefix(cfg.Remote, "manifests"))
	if err != nil {
		return result, fmt.Errorf("configure remote manifest storage: %w", err)
	}
	remotePacks, err := NewStorageEngine("", withSubPrefix(cfg.Remote, "packs"))
	if err != nil {
		return result, fmt.Errorf("configure remote pack storage: %w", err)
	}
	remoteIndex, err := NewStorageEngine("", withSubPrefix(cfg.Remote, "index"))
	if err != nil {
		return result, fmt.Errorf("configure remote index storage: %w", err)
	}

	result, err = e.recoverFromEngines(ctx, remoteManifests, remotePacks, remoteIndex, reporter)
	return result, err
}

// recoverFromEngines contains the core recovery logic, independent of how the remote manifest,
// pack, and index storage engines were constructed, so it can be exercised directly against
// in-process fakes.
func (e *Engine) recoverFromEngines(ctx context.Context, remoteManifests, remotePacks, remoteIndex pack.StorageEngine, reporter Reporter) (result RecoverResult, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()

	emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseReading, Level: EventInfo, Message: "Recovering snapshot manifests"})
	snapshotIDs, err := remoteManifests.ListPacks(ctx)
	if err != nil {
		return result, fmt.Errorf("list remote manifests: %w", err)
	}
	totalManifests := int64(len(snapshotIDs))
	for _, snapshotID := range snapshotIDs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		envelope, readErr := readAllFromPack(ctx, remoteManifests, snapshotID)
		if readErr != nil {
			return result, fmt.Errorf("read remote manifest %x: %w", snapshotID, readErr)
		}
		if err := e.idx.PutSnapshot(snapshotID, envelope); err != nil {
			return result, fmt.Errorf("store recovered manifest %x: %w", snapshotID, err)
		}
		result.ManifestsRecovered++
		emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseReading, Level: EventInfo, Message: "Recovered manifests", Completed: result.ManifestsRecovered, Total: totalManifests, Unit: "manifests"})
	}

	emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseReading, Level: EventInfo, Message: "Recovering packfiles and chunk locations"})
	packIDs, err := remotePacks.ListPacks(ctx)
	if err != nil {
		return result, fmt.Errorf("list remote packs: %w", err)
	}
	totalPacks := int64(len(packIDs))
	now := time.Now().Unix()
	for _, packID := range packIDs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := e.recoverOnePack(ctx, remotePacks, packID, now, &result); err != nil {
			return result, fmt.Errorf("recover pack %x: %w", packID, err)
		}
		result.PacksScanned++
		emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseReading, Level: EventInfo, Message: "Recovered packfiles", Completed: result.PacksScanned, Total: totalPacks, Unit: "packs"})
	}

	emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseReading, Level: EventInfo, Message: "Recovering CID directory"})
	found, restored, cidErr := e.recoverCIDDirectory(ctx, remoteIndex)
	if cidErr != nil {
		return result, fmt.Errorf("recover cid directory: %w", cidErr)
	}
	result.CIDDirectoryFound = found
	result.CIDsRecovered = restored
	if !found {
		emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseReading, Level: EventWarning, Message: "No remote CID directory found: chunks will remain undecryptable until a full rebuild from original sources"})
	}

	emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseChecking, Level: EventInfo, Message: "Verifying recovered repository"})
	verify, verifyErr := e.VerifyDetailed(ctx, reporter)
	result.Verify = verify
	if verifyErr != nil {
		return result, fmt.Errorf("verify recovered repository: %w", verifyErr)
	}

	emit(reporter, ProgressEvent{Operation: OperationRecover, Phase: PhaseComplete, Level: EventSuccess, Message: "Recovery completed"})
	return result, nil
}

func (e *Engine) recoverOnePack(ctx context.Context, remote pack.StorageEngine, packID [32]byte, uploadTimeUnix int64, result *RecoverResult) error {
	data, err := readAllFromPack(ctx, remote, packID)
	if err != nil {
		return fmt.Errorf("read remote pack: %w", err)
	}
	entries, err := pack.ParseTailIndex(data)
	if err != nil {
		return fmt.Errorf("parse pack tail index: %w", err)
	}
	if err := e.storage.PutPack(ctx, packID, bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("store recovered pack locally: %w", err)
	}
	for _, entry := range entries {
		if err := e.idx.UpsertChunkLocation(entry.StorageID, packID, entry.Offset, entry.Length, uploadTimeUnix); err != nil {
			return fmt.Errorf("store recovered chunk location: %w", err)
		}
		result.ChunkLocations++
	}
	return nil
}

func readAllFromPack(ctx context.Context, source pack.StorageEngine, id [32]byte) ([]byte, error) {
	rc, err := source.GetPack(ctx, id)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
