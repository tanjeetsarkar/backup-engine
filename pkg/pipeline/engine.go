package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/tanjeetsarkar/backup-engine/pkg/chunker"
	backupcrypto "github.com/tanjeetsarkar/backup-engine/pkg/crypto"
	"github.com/tanjeetsarkar/backup-engine/pkg/index"
	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
	"github.com/tanjeetsarkar/backup-engine/pkg/retention"
	"github.com/zeebo/blake3"
	"golang.org/x/sys/unix"
)

const defaultPackTargetSize = 16 * 1024 * 1024

// EngineConfig configures a local repository runtime.
type EngineConfig struct {
	RepoDir        string
	Passphrase     []byte
	Salt           []byte
	PackTargetSize int
	// Storage overrides the pack storage backend (e.g. MinIO/S3 via NewStorageEngine). When nil,
	// Open falls back to the local filesystem backend under RepoDir/data.
	Storage pack.StorageEngine
}

// Engine coordinates backup/restore operations over local storage.
type Engine struct {
	repoDir        string
	idx            *index.DB
	storage        pack.StorageEngine
	km             *backupcrypto.KeyManager
	packTargetSize int
	compressor     *zstd.Encoder
	decompressor   *zstd.Decoder
	dedupFilter    *bloomFilter
}

// SnapshotStatus reports whether a snapshot metadata envelope is decryptable.
type SnapshotStatus struct {
	ID         [32]byte
	Readable   bool
	StatusText string
	Timestamp  time.Time
	TotalFiles int64
	TotalBytes int64
}

// Open initializes an engine bound to a local repository.
func Open(cfg EngineConfig) (*Engine, error) {
	if cfg.RepoDir == "" {
		return nil, fmt.Errorf("repo directory is required")
	}
	if len(cfg.Passphrase) == 0 {
		return nil, fmt.Errorf("passphrase is required")
	}
	if len(cfg.Salt) < 16 {
		return nil, fmt.Errorf("salt must be at least 16 bytes")
	}
	if cfg.PackTargetSize <= 0 {
		cfg.PackTargetSize = defaultPackTargetSize
	}

	idx, err := index.Open(filepath.Join(cfg.RepoDir, "index", "index.db"))
	if err != nil {
		return nil, err
	}

	storage := cfg.Storage
	if storage == nil {
		local, err := newLocalStorageEngine(cfg.RepoDir)
		if err != nil {
			_ = idx.Close()
			return nil, err
		}
		storage = local
	}
	if err := recoverInterruptedBackup(cfg.RepoDir, idx, storage); err != nil {
		_ = idx.Close()
		return nil, fmt.Errorf("recover interrupted backup: %w", err)
	}
	if err := retention.RecoverInterruptedRepack(context.Background(), storage, idx); err != nil {
		_ = idx.Close()
		return nil, fmt.Errorf("recover interrupted gc repack: %w", err)
	}

	km, err := backupcrypto.NewKeyManager(cfg.Passphrase, cfg.Salt)
	if err != nil {
		_ = idx.Close()
		return nil, err
	}

	if err := ensureRepositoryKeyCheck(idx, km, false); err != nil {
		_ = idx.Close()
		return nil, err
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		_ = idx.Close()
		return nil, fmt.Errorf("create zstd encoder: %w", err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		enc.Close()
		_ = idx.Close()
		return nil, fmt.Errorf("create zstd decoder: %w", err)
	}

	// Built once from the existing dedup directory so backupOneFile can skip the bbolt read on a
	// definite miss; a repository-open cost proportional to distinct chunk count, not per backup.
	dedupFilter, err := buildDedupFilter(idx)
	if err != nil {
		enc.Close()
		dec.Close()
		_ = idx.Close()
		return nil, fmt.Errorf("build dedup filter: %w", err)
	}

	return &Engine{
		repoDir:        cfg.RepoDir,
		idx:            idx,
		storage:        storage,
		km:             km,
		packTargetSize: cfg.PackTargetSize,
		compressor:     enc,
		decompressor:   dec,
		dedupFilter:    dedupFilter,
	}, nil
}

// Close releases resources owned by the engine.
func (e *Engine) Close() error {
	e.compressor.Close()
	e.decompressor.Close()
	return e.idx.Close()
}

// BackupPath backs up a file or directory and commits an encrypted snapshot manifest.
func (e *Engine) BackupPath(ctx context.Context, srcPath string, parent *[32]byte, tags []string) ([32]byte, error) {
	result, err := e.BackupPathDetailed(ctx, srcPath, parent, tags, nil)
	return result.SnapshotID, err
}

// BackupPathDetailed backs up a path and reports aggregate progress and metrics.
func (e *Engine) BackupPathDetailed(ctx context.Context, srcPath string, parent *[32]byte, tags []string, reporter Reporter) (result BackupResult, err error) {
	return e.BackupPathDetailedWithOptions(ctx, srcPath, parent, tags, BackupOptions{PermissionPolicy: PermissionPolicyFail}, reporter)
}

// BackupPathDetailedWithOptions backs up a path using explicit source handling options.
func (e *Engine) BackupPathDetailedWithOptions(ctx context.Context, srcPath string, parent *[32]byte, tags []string, options BackupOptions, reporter Reporter) (result BackupResult, err error) {
	started := time.Now()
	result.RetentionTags = append([]string(nil), tags...)
	defer func() { e.recordTransaction(OperationBackup, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()

	emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseScanning, Level: EventInfo, Message: "Scanning source files"})
	scan, err := ScanBackupSource(ctx, srcPath)
	if err != nil {
		return result, err
	}
	if options.PermissionPolicy == "" {
		options.PermissionPolicy = PermissionPolicyFail
	}
	if options.PermissionPolicy != PermissionPolicyFail && options.PermissionPolicy != PermissionPolicySkip {
		return result, fmt.Errorf("invalid permission policy %q", options.PermissionPolicy)
	}
	result.Workers = resolveBackupWorkers(options.Workers)
	result.FilesScanned = int64(len(scan.Files) + len(scan.SkippedItems))
	result.FilesSkipped = int64(len(scan.SkippedItems))
	result.SkippedItems = append(result.SkippedItems, scan.SkippedItems...)
	if len(scan.SkippedItems) > 0 && options.PermissionPolicy == PermissionPolicyFail {
		return result, &PermissionPreflightError{Items: scan.SkippedItems}
	}

	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return result, err
	}
	defer lock.release()

	existingPacks, err := e.storage.ListPacks(ctx)
	if err != nil {
		return result, err
	}
	transaction := backupTransaction{preexistingPacks: make(map[[32]byte]struct{}, len(existingPacks))}
	for _, packID := range existingPacks {
		transaction.preexistingPacks[packID] = struct{}{}
	}
	if err := persistBackupJournal(e.idx, &transaction); err != nil {
		return result, err
	}
	committed := false
	defer func() {
		if !committed && err != nil {
			if rollbackErr := e.rollbackBackup(&transaction); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback backup: %w", rollbackErr))
			}
		}
	}()

	emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseProcessing, Level: EventInfo, Message: "Processing files", Total: result.FilesScanned, Unit: "files"})

	builder := pack.NewPackfileBuilder(e.packTargetSize)
	var pending []pendingChunk
	cache := make(map[[32]byte][32]byte)
	fileNodes := make([]*manifest.FileNode, len(scan.Files))
	fileStorageIDs := make([][][32]byte, len(scan.Files))
	processingContext, cancelWorkers := context.WithCancel(ctx)
	events := e.prepareBackupFiles(processingContext, srcPath, scan.Files, result.Workers)
	var processingErr error
	for event := range events {
		if processingErr != nil {
			continue
		}
		if event.err != nil {
			if event.index >= 0 && isPermissionError(event.err) && options.PermissionPolicy == PermissionPolicySkip && len(fileStorageIDs[event.index]) == 0 {
				item := SkippedItem{Path: event.path, Kind: "file", Operation: event.operation, Reason: event.err.Error()}
				result.SkippedItems = append(result.SkippedItems, item)
				result.FilesSkipped++
				result.SkippedBytes += event.size
				emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseProcessing, Level: EventWarning, Message: "Skipped unreadable source file", Completed: result.FilesProcessed + result.FilesSkipped, Total: result.FilesScanned, Unit: "files"})
				continue
			}
			processingErr = fmt.Errorf("%s %s: %w", event.operation, event.path, event.err)
			cancelWorkers()
			continue
		}
		if event.chunk != nil {
			result.ChunksExamined++
			sid, found := cache[event.chunk.cid]
			if !found && (e.dedupFilter == nil || e.dedupFilter.MightContain(event.chunk.cid)) {
				var lookupErr error
				sid, found, lookupErr = e.idx.GetStorageID(event.chunk.cid)
				if lookupErr != nil {
					processingErr = lookupErr
					cancelWorkers()
					continue
				}
			}
			if found {
				result.ChunksReused++
				cache[event.chunk.cid] = sid
				fileStorageIDs[event.index] = append(fileStorageIDs[event.index], sid)
				continue
			}
			if appendErr := builder.Append(event.chunk.storageID, event.chunk.nonce, event.chunk.ciphertext); appendErr != nil {
				processingErr = appendErr
				cancelWorkers()
				continue
			}
			pending = append(pending, pendingChunk{CID: event.chunk.cid, StorageID: event.chunk.storageID})
			result.ChunksNew++
			cache[event.chunk.cid] = event.chunk.storageID
			fileStorageIDs[event.index] = append(fileStorageIDs[event.index], event.chunk.storageID)
			if e.dedupFilter != nil {
				e.dedupFilter.Add(event.chunk.cid)
			}
			if builder.IsFull() {
				emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseWriting, Level: EventInfo, Message: "Writing encrypted pack"})
				if flushErr := e.flushPackDetailed(processingContext, builder, pending, &result, &transaction); flushErr != nil {
					processingErr = flushErr
					cancelWorkers()
					continue
				}
				builder = pack.NewPackfileBuilder(e.packTargetSize)
				pending = nil
			}
			continue
		}
		if event.node != nil {
			event.node.StorageIDs = fileStorageIDs[event.index]
			fileNodes[event.index] = event.node
			result.FilesProcessed++
			result.LogicalBytes += event.node.Size
			emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseProcessing, Level: EventInfo, Message: "Processed source files", Completed: result.FilesProcessed + result.FilesSkipped, Total: result.FilesScanned, Unit: "files"})
		}
	}
	cancelWorkers()
	if processingErr != nil {
		return result, processingErr
	}

	orderedNodes := make([]*manifest.FileNode, 0, result.FilesProcessed)
	for _, node := range fileNodes {
		if node != nil {
			orderedNodes = append(orderedNodes, node)
		}
	}

	if len(pending) > 0 {
		emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseWriting, Level: EventInfo, Message: "Writing encrypted pack"})
		if err := e.flushPackDetailed(ctx, builder, pending, &result, &transaction); err != nil {
			return result, err
		}
	}

	emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseCommitting, Level: EventInfo, Message: "Committing snapshot manifest"})
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root := &manifest.DirectoryNode{Path: "/", Files: orderedNodes}
	snapshot, err := manifest.NewSnapshotManifest(parent, root, tags)
	if err != nil {
		return result, err
	}
	env, err := manifest.EncryptSnapshotEnvelope(snapshot, e.km)
	if err != nil {
		return result, err
	}
	if err := e.idx.CommitSnapshot(snapshot.SnapshotID, env, backupJournalMetaKey); err != nil {
		return result, err
	}
	committed = true

	result.SnapshotID = snapshot.SnapshotID
	emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseComplete, Level: EventSuccess, Message: "Backup completed", Completed: result.FilesProcessed, Total: result.FilesScanned, Unit: "files"})
	return result, nil
}

// RestoreSnapshot restores a stored snapshot into destination root.
func (e *Engine) RestoreSnapshot(ctx context.Context, snapshotID [32]byte, destRoot string) error {
	_, err := e.RestoreSnapshotDetailed(ctx, snapshotID, destRoot, nil)
	return err
}

// RestoreSnapshotDetailed restores a snapshot and reports aggregate progress and metrics.
func (e *Engine) RestoreSnapshotDetailed(ctx context.Context, snapshotID [32]byte, destRoot string, reporter Reporter) (result RestoreResult, err error) {
	started := time.Now()
	result.SnapshotID = snapshotID
	result.Destination = destRoot
	defer func() { e.recordTransaction(OperationRestore, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()
	emit(reporter, ProgressEvent{Operation: OperationRestore, Phase: PhasePreparing, Level: EventInfo, Message: "Loading snapshot manifest"})

	env, found, err := e.idx.GetSnapshot(snapshotID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, fmt.Errorf("snapshot not found")
	}

	snapshot, err := manifest.DecryptSnapshotEnvelope(env, e.km)
	if err != nil {
		if errors.Is(err, backupcrypto.ErrAuthenticationFailed) {
			return result, fmt.Errorf("snapshot metadata authentication failed: likely wrong passphrase/salt for this repository or corrupted snapshot metadata")
		}
		return result, err
	}
	if snapshot.Root == nil {
		return result, fmt.Errorf("snapshot root is nil")
	}

	totalFiles := int64(len(snapshot.Root.Files))
	emit(reporter, ProgressEvent{Operation: OperationRestore, Phase: PhaseReading, Level: EventInfo, Message: "Restoring files", Total: totalFiles, Unit: "files"})
	for _, fileNode := range snapshot.Root.Files {
		if err := e.restoreOneFileDetailed(ctx, destRoot, fileNode, &result); err != nil {
			return result, err
		}
		result.FilesRestored++
		result.LogicalBytes += fileNode.Size
		emit(reporter, ProgressEvent{Operation: OperationRestore, Phase: PhaseReading, Level: EventInfo, Message: "Restored files", Completed: result.FilesRestored, Total: totalFiles, Unit: "files"})
	}

	emit(reporter, ProgressEvent{Operation: OperationRestore, Phase: PhaseComplete, Level: EventSuccess, Message: "Restore completed", Completed: result.FilesRestored, Total: totalFiles, Unit: "files"})
	return result, nil
}

// ListSnapshots returns known snapshot IDs.
func (e *Engine) ListSnapshots() ([][32]byte, error) {
	ids, err := e.idx.ListSnapshotIDs()
	if err != nil {
		return nil, err
	}

	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})

	return ids, nil
}

// ListSnapshotStatuses lists snapshots and whether their metadata can be decrypted with the current key.
func (e *Engine) ListSnapshotStatuses() ([]SnapshotStatus, error) {
	details, err := e.ListSnapshotDetails(true)
	if err != nil {
		return nil, err
	}
	statuses := make([]SnapshotStatus, 0, len(details))
	for _, detail := range details {
		statuses = append(statuses, SnapshotStatus{ID: detail.ID, Readable: detail.Readable, StatusText: detail.StatusText, Timestamp: detail.Timestamp, TotalFiles: detail.TotalFiles, TotalBytes: detail.TotalBytes})
	}
	return statuses, nil
}

// RunGC applies GFS retention and performs sweep/compaction.
func (e *Engine) RunGC(ctx context.Context, policy retention.GFSPolicy, graceLimit time.Duration) (int, error) {
	result, err := e.RunGCDetailed(ctx, policy, graceLimit, nil)
	return int(result.ChunksPurged), err
}

// RunGCDetailed applies retention and returns its evaluated decisions and aggregate metrics.
func (e *Engine) RunGCDetailed(ctx context.Context, policy retention.GFSPolicy, graceLimit time.Duration, reporter Reporter) (result GCResult, err error) {
	started := time.Now()
	defer func() { e.recordTransaction(OperationGC, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()
	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return result, err
	}
	defer lock.release()
	emit(reporter, ProgressEvent{Operation: OperationGC, Phase: PhasePlanning, Level: EventInfo, Message: "Evaluating snapshot retention"})

	ids, err := e.idx.ListSnapshotIDs()
	if err != nil {
		return result, err
	}

	manifests := make([]*manifest.SnapshotManifest, 0, len(ids))
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
		manifests = append(manifests, snap)
	}

	classified := retention.EvaluateGFS(manifests, policy)
	result.SnapshotsEvaluated = int64(len(classified))
	retained := make([]*manifest.SnapshotManifest, 0, len(classified))
	purgedSnapshotIDs := make([][32]byte, 0)
	now := time.Now()
	for _, item := range classified {
		lifecycle, lifecycleErr := e.readSnapshotLifecycle(item.Manifest.SnapshotID)
		if lifecycleErr != nil {
			return result, lifecycleErr
		}
		keep := item.Keep
		reason := item.Reason
		if lifecycle.State == SnapshotTrashed {
			keep = lifecycle.PurgeAfter == nil || lifecycle.PurgeAfter.After(now)
			if keep {
				reason = "Recoverable trash"
			} else {
				reason = "Trash recovery window expired"
			}
		} else if lifecycle.Pinned {
			keep = true
			reason = "Pinned"
		} else if lifecycle.RetainUntil != nil && lifecycle.RetainUntil.After(now) {
			keep = true
			reason = "Retain-until override"
		}
		decision := RetentionDecision{SnapshotID: item.Manifest.SnapshotID, Keep: keep, Reason: reason}
		result.Decisions = append(result.Decisions, decision)
		if keep {
			retained = append(retained, item.Manifest)
			result.SnapshotsRetained++
		} else {
			result.SnapshotsDropped++
			purgedSnapshotIDs = append(purgedSnapshotIDs, item.Manifest.SnapshotID)
		}
	}

	records, err := e.idx.ListChunkRecords()
	if err != nil {
		return result, err
	}
	result.ChunksExamined = int64(len(records))
	chunks := make([]retention.ChunkLocation, 0, len(records))
	for _, r := range records {
		chunks = append(chunks, retention.ChunkLocation{
			PackID:         r.Location.PackID,
			StorageID:      r.StorageID,
			Offset:         r.Location.Offset,
			Length:         r.Location.Length,
			UploadTimeUnix: r.Location.UploadTimeUnix,
		})
	}

	gc := retention.NewGarbageCollector(e.storage, e.idx, graceLimit)
	emit(reporter, ProgressEvent{Operation: OperationGC, Phase: PhaseCompacting, Level: EventInfo, Message: "Sweeping unreferenced chunks", Total: result.ChunksExamined, Unit: "chunks"})
	purged, err := gc.Run(ctx, retained, chunks)
	result.ChunksPurged = int64(purged)
	if err != nil {
		return result, err
	}
	if err := e.idx.DeleteSnapshots(purgedSnapshotIDs); err != nil {
		return result, fmt.Errorf("delete expired snapshot metadata: %w", err)
	}
	emit(reporter, ProgressEvent{Operation: OperationGC, Phase: PhaseComplete, Level: EventSuccess, Message: "Garbage collection completed", Completed: result.ChunksPurged, Unit: "chunks"})
	return result, nil
}

// Verify validates that all snapshots are decryptable and all referenced data chunks are readable/authentic.
func (e *Engine) Verify(ctx context.Context) error {
	_, err := e.VerifyDetailed(ctx, nil)
	return err
}

// VerifyDetailed validates repository data and reports aggregate progress and metrics.
func (e *Engine) VerifyDetailed(ctx context.Context, reporter Reporter) (result VerifyResult, err error) {
	started := time.Now()
	defer func() { e.recordTransaction(OperationVerify, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()
	emit(reporter, ProgressEvent{Operation: OperationVerify, Phase: PhasePreparing, Level: EventInfo, Message: "Loading snapshot catalog"})

	ids, err := e.idx.ListSnapshotIDs()
	if err != nil {
		return result, err
	}
	totalSnapshots := int64(len(ids))
	emit(reporter, ProgressEvent{Operation: OperationVerify, Phase: PhaseChecking, Level: EventInfo, Message: "Checking snapshots and referenced data", Total: totalSnapshots, Unit: "snapshots"})

	for _, id := range ids {
		env, found, getErr := e.idx.GetSnapshot(id)
		if getErr != nil {
			return result, getErr
		}
		if !found {
			return result, fmt.Errorf("snapshot disappeared during verify")
		}

		snap, decErr := manifest.DecryptSnapshotEnvelope(env, e.km)
		if decErr != nil {
			return result, fmt.Errorf("snapshot %x decrypt failed: %w", id, decErr)
		}
		if snap.Root == nil {
			return result, fmt.Errorf("snapshot %x has nil root", id)
		}

		for _, fileNode := range snap.Root.Files {
			if _, matErr := e.materializeFileContentDetailed(ctx, fileNode, func() { result.ChunksChecked++ }); matErr != nil {
				return result, fmt.Errorf("snapshot %x file %s verify failed: %w", id, fileNode.Path, matErr)
			}
			result.FilesChecked++
			result.LogicalBytes += fileNode.Size
		}
		result.SnapshotsChecked++
		emit(reporter, ProgressEvent{Operation: OperationVerify, Phase: PhaseChecking, Level: EventInfo, Message: "Checked snapshots", Completed: result.SnapshotsChecked, Total: totalSnapshots, Unit: "snapshots"})
	}

	emit(reporter, ProgressEvent{Operation: OperationVerify, Phase: PhaseComplete, Level: EventSuccess, Message: "Verification completed", Completed: result.SnapshotsChecked, Total: totalSnapshots, Unit: "snapshots"})
	return result, nil
}

// Doctor runs consistency checks on index and snapshot references.
func (e *Engine) Doctor(ctx context.Context) error {
	result, err := e.DoctorDetailed(ctx, nil)
	if err != nil {
		return err
	}
	if len(result.Issues) == 0 {
		return nil
	}
	messages := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		messages = append(messages, issue.Summary)
	}
	return errors.New(strings.Join(messages, "; "))
}

// DoctorDetailed checks index and data consistency and returns structured issues.
func (e *Engine) DoctorDetailed(ctx context.Context, reporter Reporter) (result DoctorResult, err error) {
	started := time.Now()
	defer func() { e.recordTransaction(OperationDoctor, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()
	emit(reporter, ProgressEvent{Operation: OperationDoctor, Phase: PhaseChecking, Level: EventInfo, Message: "Checking chunk index and pack records"})

	records, err := e.idx.ListChunkRecords()
	if err != nil {
		return result, err
	}

	for _, r := range records {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.ChunkRecordsChecked++

		cid, found, getErr := e.idx.GetCID(r.StorageID)
		if getErr != nil {
			result.Issues = append(result.Issues, doctorIssue("reverse-lookup", r.StorageID, "Reverse chunk lookup failed", getErr.Error(), "Run doctor again; restore the repository index from a known-good copy if the error persists."))
			continue
		}
		if !found {
			result.Issues = append(result.Issues, doctorIssue("missing-reverse-mapping", r.StorageID, "Chunk is missing its reverse CID mapping", "", "Restore the index from a known-good copy before running garbage collection."))
			continue
		}

		sid, found, getErr := e.idx.GetStorageID(cid)
		if getErr != nil {
			result.Issues = append(result.Issues, doctorIssue("forward-lookup", r.StorageID, "Forward chunk lookup failed", getErr.Error(), "Run doctor again; restore the repository index if the error persists."))
			continue
		}
		if !found || sid != r.StorageID {
			result.Issues = append(result.Issues, doctorIssue("mapping-mismatch", r.StorageID, "CID and storage mappings do not agree", "", "Do not run garbage collection until the index is repaired."))
		}

		raw, readErr := e.storage.GetChunkRange(ctx, r.Location.PackID, int64(r.Location.Offset), pack.RecordSizeFromCipherLen(r.Location.Length))
		if readErr != nil {
			result.Issues = append(result.Issues, doctorIssue("unreadable-record", r.StorageID, "Pack record cannot be read", readErr.Error(), "Check repository permissions and storage integrity, then run verify."))
			continue
		}

		recSID, _, _, decErr := pack.DecodeChunkRecord(raw)
		if decErr != nil {
			result.Issues = append(result.Issues, doctorIssue("record-decode", r.StorageID, "Pack record cannot be decoded", decErr.Error(), "Restore the affected pack from another repository copy."))
			continue
		}
		if recSID != r.StorageID {
			result.Issues = append(result.Issues, doctorIssue("storage-id-mismatch", r.StorageID, "Pack record storage ID does not match the index", "", "Restore the affected pack and index from a consistent copy."))
		}
	}

	verify, verifyErr := e.VerifyDetailed(ctx, reporter)
	result.Verify = verify
	if verifyErr != nil {
		result.Issues = append(result.Issues, DoctorIssue{Code: "verify-failed", Severity: "error", Summary: "Repository verification failed", Detail: verifyErr.Error(), Hint: "Inspect the failing snapshot or pack and restore it from another repository copy."})
	}

	level := EventSuccess
	message := "Repository health checks completed"
	if len(result.Issues) > 0 {
		level = EventWarning
		message = "Repository health checks found issues"
	}
	emit(reporter, ProgressEvent{Operation: OperationDoctor, Phase: PhaseComplete, Level: level, Message: message, Completed: result.ChunkRecordsChecked, Total: int64(len(records)), Unit: "records"})
	return result, nil
}

func doctorIssue(code string, storageID [32]byte, summary, detail, hint string) DoctorIssue {
	return DoctorIssue{Code: code, Severity: "error", Resource: fmt.Sprintf("%x", storageID), Summary: summary, Detail: detail, Hint: hint}
}

type pendingChunk struct {
	CID       [32]byte
	StorageID [32]byte
}

type backupTransaction struct {
	preexistingPacks map[[32]byte]struct{}
	newPacks         [][32]byte
	newStorageIDs    [][32]byte
}

type backupFileJob struct {
	index int
	path  string
}

type preparedChunk struct {
	cid        [32]byte
	storageID  [32]byte
	nonce      []byte
	ciphertext []byte
}

type backupFileEvent struct {
	index     int
	path      string
	operation string
	size      int64
	node      *manifest.FileNode
	chunk     *preparedChunk
	err       error
}

func resolveBackupWorkers(configured int) int {
	if configured > 0 {
		return configured
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

func (e *Engine) prepareBackupFiles(ctx context.Context, basePath string, files []string, workers int) <-chan backupFileEvent {
	jobs := make(chan backupFileJob)
	events := make(chan backupFileEvent, workers*2)
	var workerGroup sync.WaitGroup
	workerGroup.Add(workers)
	for range workers {
		go func() {
			defer workerGroup.Done()
			encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
			if err != nil {
				select {
				case events <- backupFileEvent{index: -1, operation: "create compressor", err: err}:
				case <-ctx.Done():
				}
				return
			}
			defer encoder.Close()
			for job := range jobs {
				e.prepareBackupFile(ctx, basePath, job, encoder, events)
				if ctx.Err() != nil {
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index, path := range files {
			select {
			case jobs <- backupFileJob{index: index, path: path}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workerGroup.Wait()
		close(events)
	}()
	return events
}

func (e *Engine) prepareBackupFile(ctx context.Context, basePath string, job backupFileJob, encoder *zstd.Encoder, events chan<- backupFileEvent) {
	send := func(event backupFileEvent) bool {
		event.index = job.index
		event.path = job.path
		select {
		case events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}

	filePath := job.path
	relPath := filePath
	if fi, statErr := os.Stat(basePath); statErr == nil && fi.IsDir() {
		var relErr error
		relPath, relErr = filepath.Rel(basePath, filePath)
		if relErr != nil {
			send(backupFileEvent{operation: "resolve path", err: relErr})
			return
		}
	}
	if relPath == "." {
		relPath = filepath.Base(filePath)
	}
	relPath = filepath.ToSlash(relPath)

	linkInfo, err := os.Lstat(filePath)
	if err != nil {
		send(backupFileEvent{operation: "inspect", err: err})
		return
	}
	xattrs, err := captureXAttrs(filePath)
	if err != nil {
		if !isPermissionError(err) {
			send(backupFileEvent{operation: "read metadata", size: linkInfo.Size(), err: err})
			return
		}
		xattrs = nil
	}
	uid, gid := fileOwnership(linkInfo)

	if linkInfo.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(filePath)
		if readErr != nil {
			send(backupFileEvent{operation: "read symlink", err: readErr})
			return
		}
		node := &manifest.FileNode{
			Path:          relPath,
			Mode:          uint32(linkInfo.Mode().Perm()),
			ModTimeEpoch:  linkInfo.ModTime().Unix(),
			UID:           uid,
			GID:           gid,
			SymlinkTarget: target,
			XAttrs:        xattrs,
		}
		send(backupFileEvent{node: node})
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		send(backupFileEvent{operation: "open", size: linkInfo.Size(), err: err})
		return
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		send(backupFileEvent{operation: "inspect open file", size: linkInfo.Size(), err: err})
		return
	}

	contentHasher := blake3.New()
	ch := chunker.NewFastCDC(io.TeeReader(file, contentHasher))
	for {
		if ctx.Err() != nil {
			return
		}

		piece, err := ch.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			send(backupFileEvent{operation: "read", size: fileInfo.Size(), err: err})
			return
		}

		cid := backupcrypto.ComputeCID(piece.Data)
		sid := e.km.DeriveStorageID(cid)
		chunkKey := e.km.DeriveChunkKey(cid)
		compressed := encoder.EncodeAll(piece.Data, nil)
		ciphertext, nonce, err := backupcrypto.EncryptChunk(compressed, chunkKey, sid)
		if err != nil {
			send(backupFileEvent{operation: "encrypt", size: fileInfo.Size(), err: err})
			return
		}
		if !send(backupFileEvent{chunk: &preparedChunk{cid: cid, storageID: sid, nonce: nonce, ciphertext: ciphertext}}) {
			return
		}
	}

	var contentHash [32]byte
	copy(contentHash[:], contentHasher.Sum(nil))
	node := &manifest.FileNode{
		Path:         relPath,
		Size:         fileInfo.Size(),
		Mode:         uint32(fileInfo.Mode().Perm()),
		ModTimeEpoch: fileInfo.ModTime().Unix(),
		ContentHash:  contentHash,
		UID:          uid,
		GID:          gid,
		XAttrs:       xattrs,
	}
	send(backupFileEvent{node: node})
}

func (e *Engine) flushPack(ctx context.Context, builder *pack.PackfileBuilder, pending []pendingChunk) error {
	return e.flushPackDetailed(ctx, builder, pending, nil, nil)
}

func (e *Engine) flushPackDetailed(ctx context.Context, builder *pack.PackfileBuilder, pending []pendingChunk, result *BackupResult, transaction *backupTransaction) error {
	if len(pending) == 0 {
		return nil
	}

	packBytes, packID, entries, err := builder.Finalize()
	if err != nil {
		return err
	}
	if len(entries) != len(pending) {
		return fmt.Errorf("pending chunk metadata mismatch")
	}

	preexisting := false
	if transaction != nil {
		_, preexisting = transaction.preexistingPacks[packID]
		if !preexisting {
			transaction.newPacks = append(transaction.newPacks, packID)
		}
		for _, meta := range pending {
			transaction.newStorageIDs = append(transaction.newStorageIDs, meta.StorageID)
		}
		if err := persistBackupJournal(e.idx, transaction); err != nil {
			return err
		}
	}
	if !preexisting {
		if err := e.storage.PutPack(ctx, packID, bytes.NewReader(packBytes), int64(len(packBytes))); err != nil {
			return err
		}
		if result != nil {
			result.PacksWritten++
			result.StoredBytes += int64(len(packBytes))
		}
	}

	now := time.Now().Unix()
	mappings := make([]index.ChunkMapping, 0, len(entries))
	for i, entry := range entries {
		meta := pending[i]
		mappings = append(mappings, index.ChunkMapping{CID: meta.CID, StorageID: meta.StorageID, Location: index.ChunkLocation{
			PackID:         packID,
			Offset:         entry.Offset,
			Length:         entry.Length,
			UploadTimeUnix: now,
		}})
	}
	if err := e.idx.PutChunkMappings(mappings); err != nil {
		return err
	}

	return nil
}

func (e *Engine) rollbackBackup(transaction *backupTransaction) error {
	if transaction == nil {
		return nil
	}
	var rollbackErr error
	for index := len(transaction.newStorageIDs) - 1; index >= 0; index-- {
		rollbackErr = errors.Join(rollbackErr, e.idx.DeleteChunk(transaction.newStorageIDs[index]))
	}
	for index := len(transaction.newPacks) - 1; index >= 0; index-- {
		if err := e.storage.DeletePack(context.Background(), transaction.newPacks[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	if rollbackErr == nil {
		rollbackErr = e.idx.DeleteMeta(backupJournalMetaKey)
	}
	return rollbackErr
}

func (e *Engine) restoreOneFile(ctx context.Context, destRoot string, fileNode *manifest.FileNode) error {
	return e.restoreOneFileDetailed(ctx, destRoot, fileNode, nil)
}

func (e *Engine) restoreOneFileDetailed(ctx context.Context, destRoot string, fileNode *manifest.FileNode, result *RestoreResult) error {
	if fileNode == nil {
		return nil
	}

	cleanRel := strings.TrimPrefix(fileNode.Path, "/")
	outPath := filepath.Join(destRoot, filepath.FromSlash(cleanRel))
	if err := os.MkdirAll(filepath.Dir(outPath), 0750); err != nil {
		return err
	}

	if fileNode.IsSymlink() {
		_ = os.Remove(outPath) // clear any leftover entry so Symlink does not fail with EEXIST
		if err := os.Symlink(fileNode.SymlinkTarget, outPath); err != nil {
			return err
		}
		if err := applyXAttrs(outPath, fileNode.XAttrs); err != nil {
			return err
		}
		if err := applyOwnership(outPath, fileNode.UID, fileNode.GID); err != nil {
			return err
		}
		return applySymlinkModTime(outPath, fileNode.ModTimeEpoch)
	}

	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(fileNode.Mode))
	if err != nil {
		return err
	}
	defer f.Close()

	content, err := e.materializeFileContentDetailed(ctx, fileNode, func() {
		if result != nil {
			result.ChunksRead++
		}
	})
	if err != nil {
		return err
	}

	if _, err := f.Write(content); err != nil {
		return err
	}

	if err := applyXAttrs(outPath, fileNode.XAttrs); err != nil {
		return err
	}
	if err := applyOwnership(outPath, fileNode.UID, fileNode.GID); err != nil {
		return err
	}

	if err := os.Chtimes(outPath, time.Unix(fileNode.ModTimeEpoch, 0), time.Unix(fileNode.ModTimeEpoch, 0)); err != nil {
		return err
	}

	return nil
}

// fileOwnership extracts POSIX uid/gid from a Lstat result; unsupported platforms report 0,0.
func fileOwnership(info os.FileInfo) (uid, gid uint32) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Uid, stat.Gid
	}
	return 0, 0
}

// applyOwnership best-effort restores uid/gid, ignoring permission failures when not privileged.
func applyOwnership(path string, uid, gid uint32) error {
	if uid == 0 && gid == 0 {
		return nil
	}
	if err := os.Lchown(path, int(uid), int(gid)); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) {
			return nil
		}
		return err
	}
	return nil
}

// applySymlinkModTime sets a symlink's own mtime without following it, unlike os.Chtimes.
func applySymlinkModTime(path string, epoch int64) error {
	ts := unix.NsecToTimeval(time.Unix(epoch, 0).UnixNano())
	return unix.Lutimes(path, []unix.Timeval{ts, ts})
}

// captureXAttrs reads all extended attributes of path (without following symlinks); a
// filesystem that does not support xattrs yields an empty result rather than an error.
func captureXAttrs(path string) ([]manifest.XAttr, error) {
	size, err := unix.Llistxattr(path, nil)
	if err != nil {
		if isXattrUnsupported(err) {
			return nil, nil
		}
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	namesBuf := make([]byte, size)
	n, err := unix.Llistxattr(path, namesBuf)
	if err != nil {
		if isXattrUnsupported(err) {
			return nil, nil
		}
		return nil, err
	}

	attrs := make([]manifest.XAttr, 0)
	for _, name := range splitXattrNames(namesBuf[:n]) {
		valueSize, err := unix.Lgetxattr(path, name, nil)
		if err != nil {
			if isXattrUnsupported(err) {
				continue
			}
			return nil, err
		}
		if valueSize == 0 {
			attrs = append(attrs, manifest.XAttr{Name: name})
			continue
		}
		valueBuf := make([]byte, valueSize)
		vn, err := unix.Lgetxattr(path, name, valueBuf)
		if err != nil {
			if isXattrUnsupported(err) {
				continue
			}
			return nil, err
		}
		attrs = append(attrs, manifest.XAttr{Name: name, Value: valueBuf[:vn]})
	}
	return attrs, nil
}

// applyXAttrs restores previously captured extended attributes onto a restored path.
func applyXAttrs(path string, attrs []manifest.XAttr) error {
	for _, attr := range attrs {
		if err := unix.Lsetxattr(path, attr.Name, attr.Value, 0); err != nil {
			if isXattrUnsupported(err) || errors.Is(err, os.ErrPermission) {
				continue
			}
			return fmt.Errorf("restore xattr %q: %w", attr.Name, err)
		}
	}
	return nil
}

func isXattrUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENODATA)
}

func splitXattrNames(buf []byte) []string {
	names := make([]string, 0)
	start := 0
	for i, b := range buf {
		if b == 0 {
			if i > start {
				names = append(names, string(buf[start:i]))
			}
			start = i + 1
		}
	}
	return names
}

func (e *Engine) materializeFileContent(ctx context.Context, fileNode *manifest.FileNode) ([]byte, error) {
	return e.materializeFileContentDetailed(ctx, fileNode, nil)
}

func (e *Engine) materializeFileContentDetailed(ctx context.Context, fileNode *manifest.FileNode, chunkRead func()) ([]byte, error) {
	if fileNode == nil {
		return nil, nil
	}

	content := make([]byte, 0)
	for _, sid := range fileNode.StorageIDs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		loc, found, err := e.idx.GetChunkLocation(sid)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("chunk location not found for storage id")
		}

		cid, found, err := e.idx.GetCID(sid)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("cid not found for storage id")
		}

		recordSize := pack.RecordSizeFromCipherLen(loc.Length)
		rawRecord, err := e.storage.GetChunkRange(ctx, loc.PackID, int64(loc.Offset), recordSize)
		if err != nil {
			return nil, err
		}

		recordedSID, nonce, ciphertext, err := pack.DecodeChunkRecord(rawRecord)
		if err != nil {
			return nil, err
		}
		if recordedSID != sid {
			return nil, fmt.Errorf("storage id mismatch while restoring")
		}

		chunkKey := e.km.DeriveChunkKey(cid)
		compressed, err := backupcrypto.DecryptChunk(ciphertext, nonce, chunkKey, sid)
		if err != nil {
			return nil, err
		}
		plain, err := e.decompressor.DecodeAll(compressed, nil)
		if err != nil {
			return nil, err
		}

		content = append(content, plain...)
		if chunkRead != nil {
			chunkRead()
		}
	}

	gotHash := backupcrypto.ComputeCID(content)
	if gotHash != fileNode.ContentHash {
		return nil, fmt.Errorf("restored content hash mismatch for %s", fileNode.Path)
	}

	return content, nil
}

func collectFiles(srcPath string) ([]string, error) {
	return collectFilesContext(context.Background(), srcPath)
}

func collectFilesContext(ctx context.Context, srcPath string) ([]string, error) {
	scan, err := ScanBackupSource(ctx, srcPath)
	return scan.Files, err
}

// PermissionPreflightError reports source entries that require an explicit skip decision.
type PermissionPreflightError struct {
	Items []SkippedItem
}

func (e *PermissionPreflightError) Error() string {
	return fmt.Sprintf("backup source contains %d unreadable item(s)", len(e.Items))
}

func (e *PermissionPreflightError) Unwrap() error {
	return os.ErrPermission
}

func isPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}

// ScanBackupSource discovers readable source files without modifying the repository.
func ScanBackupSource(ctx context.Context, srcPath string) (BackupScanResult, error) {
	var result BackupScanResult
	info, err := os.Lstat(srcPath)
	if err != nil {
		return result, err
	}

	if !info.IsDir() {
		if info.Mode().IsRegular() {
			file, openErr := os.Open(srcPath)
			if openErr != nil {
				return result, openErr
			}
			if closeErr := file.Close(); closeErr != nil {
				return result, closeErr
			}
		}
		result.Files = []string{srcPath}
		return result, nil
	}

	err = filepath.WalkDir(srcPath, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrPermission) || errors.Is(walkErr, syscall.EPERM) {
				kind := "file"
				if d == nil || d.IsDir() {
					kind = "directory"
				}
				result.SkippedItems = append(result.SkippedItems, SkippedItem{Path: path, Kind: kind, Operation: "scan", Reason: walkErr.Error()})
				if kind == "directory" {
					return fs.SkipDir
				}
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type().IsRegular() {
			file, openErr := os.Open(path)
			if openErr != nil {
				if isPermissionError(openErr) {
					result.SkippedItems = append(result.SkippedItems, SkippedItem{Path: path, Kind: "file", Operation: "open", Reason: openErr.Error()})
					return nil
				}
				return openErr
			}
			if closeErr := file.Close(); closeErr != nil {
				return closeErr
			}
		}
		result.Files = append(result.Files, path)
		return nil
	})
	if err != nil {
		return result, err
	}

	sort.Strings(result.Files)
	sort.Slice(result.SkippedItems, func(i, j int) bool { return result.SkippedItems[i].Path < result.SkippedItems[j].Path })
	return result, nil
}
