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
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/tanjeetsarkar/backup-engine/pkg/chunker"
	backupcrypto "github.com/tanjeetsarkar/backup-engine/pkg/crypto"
	"github.com/tanjeetsarkar/backup-engine/pkg/index"
	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
	"github.com/tanjeetsarkar/backup-engine/pkg/retention"
)

const defaultPackTargetSize = 16 * 1024 * 1024

// EngineConfig configures a local repository runtime.
type EngineConfig struct {
	RepoDir        string
	Passphrase     []byte
	Salt           []byte
	PackTargetSize int
}

// Engine coordinates backup/restore operations over local storage.
type Engine struct {
	idx            *index.DB
	storage        pack.StorageEngine
	km             *backupcrypto.KeyManager
	packTargetSize int
	compressor     *zstd.Encoder
	decompressor   *zstd.Decoder
}

// SnapshotStatus reports whether a snapshot metadata envelope is decryptable.
type SnapshotStatus struct {
	ID         [32]byte
	Readable   bool
	StatusText string
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

	storage, err := pack.NewLocalFilesystemStorage(filepath.Join(cfg.RepoDir, "data"))
	if err != nil {
		_ = idx.Close()
		return nil, err
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

	return &Engine{
		idx:            idx,
		storage:        storage,
		km:             km,
		packTargetSize: cfg.PackTargetSize,
		compressor:     enc,
		decompressor:   dec,
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
	started := time.Now()
	result.RetentionTags = append([]string(nil), tags...)
	defer func() { result.Duration = time.Since(started) }()

	emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseScanning, Level: EventInfo, Message: "Scanning source files"})

	files, err := collectFiles(srcPath)
	if err != nil {
		return result, err
	}
	result.FilesScanned = int64(len(files))
	emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseProcessing, Level: EventInfo, Message: "Processing files", Total: result.FilesScanned, Unit: "files"})

	builder := pack.NewPackfileBuilder(e.packTargetSize)
	var pending []pendingChunk
	cache := make(map[[32]byte][32]byte)
	fileNodes := make([]*manifest.FileNode, 0, len(files))

	for _, filePath := range files {
		node, newPending, flushErr := e.backupOneFile(ctx, srcPath, filePath, builder, pending, cache, &result)
		if flushErr != nil {
			return result, flushErr
		}
		fileNodes = append(fileNodes, node)
		pending = newPending
		result.FilesProcessed++
		result.LogicalBytes += node.Size
		emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseProcessing, Level: EventInfo, Message: "Processed source files", Completed: result.FilesProcessed, Total: result.FilesScanned, Unit: "files"})

		if builder.IsFull() {
			emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseWriting, Level: EventInfo, Message: "Writing encrypted pack"})
			if err := e.flushPackDetailed(ctx, builder, pending, &result); err != nil {
				return result, err
			}
			builder = pack.NewPackfileBuilder(e.packTargetSize)
			pending = nil
		}
	}

	if len(pending) > 0 {
		emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseWriting, Level: EventInfo, Message: "Writing encrypted pack"})
		if err := e.flushPackDetailed(ctx, builder, pending, &result); err != nil {
			return result, err
		}
	}

	emit(reporter, ProgressEvent{Operation: OperationBackup, Phase: PhaseCommitting, Level: EventInfo, Message: "Committing snapshot manifest"})
	root := &manifest.DirectoryNode{Path: "/", Files: fileNodes}
	snapshot, err := manifest.NewSnapshotManifest(parent, root, tags)
	if err != nil {
		return result, err
	}
	env, err := manifest.EncryptSnapshotEnvelope(snapshot, e.km)
	if err != nil {
		return result, err
	}
	if err := e.idx.PutSnapshot(snapshot.SnapshotID, env); err != nil {
		return result, err
	}

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
	ids, err := e.ListSnapshots()
	if err != nil {
		return nil, err
	}

	statuses := make([]SnapshotStatus, 0, len(ids))
	for _, id := range ids {
		env, found, getErr := e.idx.GetSnapshot(id)
		if getErr != nil {
			statuses = append(statuses, SnapshotStatus{ID: id, Readable: false, StatusText: "index-read-error"})
			continue
		}
		if !found {
			statuses = append(statuses, SnapshotStatus{ID: id, Readable: false, StatusText: "missing"})
			continue
		}

		_, decErr := manifest.DecryptSnapshotEnvelope(env, e.km)
		if decErr != nil {
			if errors.Is(decErr, backupcrypto.ErrAuthenticationFailed) {
				statuses = append(statuses, SnapshotStatus{ID: id, Readable: false, StatusText: "auth-failed"})
				continue
			}
			statuses = append(statuses, SnapshotStatus{ID: id, Readable: false, StatusText: "decode-error"})
			continue
		}

		statuses = append(statuses, SnapshotStatus{ID: id, Readable: true, StatusText: "ok"})
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
	defer func() { result.Duration = time.Since(started) }()
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
	for _, item := range classified {
		decision := RetentionDecision{SnapshotID: item.Manifest.SnapshotID, Keep: item.Keep, Reason: item.Reason}
		result.Decisions = append(result.Decisions, decision)
		if item.Keep {
			retained = append(retained, item.Manifest)
			result.SnapshotsRetained++
		} else {
			result.SnapshotsDropped++
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

func (e *Engine) backupOneFile(
	ctx context.Context,
	basePath string,
	filePath string,
	builder *pack.PackfileBuilder,
	pending []pendingChunk,
	cache map[[32]byte][32]byte,
	result *BackupResult,
) (*manifest.FileNode, []pendingChunk, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, pending, err
	}

	relPath := filePath
	if fi, statErr := os.Stat(basePath); statErr == nil && fi.IsDir() {
		relPath, err = filepath.Rel(basePath, filePath)
		if err != nil {
			return nil, pending, err
		}
	}
	if relPath == "." {
		relPath = filepath.Base(filePath)
	}
	relPath = filepath.ToSlash(relPath)

	ch := chunker.NewFastCDC(bytes.NewReader(data))
	storageIDs := make([][32]byte, 0)

	for {
		if ctx.Err() != nil {
			return nil, pending, ctx.Err()
		}

		piece, err := ch.NextChunk()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, pending, err
		}

		cid := backupcrypto.ComputeCID(piece.Data)
		result.ChunksExamined++
		if sid, ok := cache[cid]; ok {
			result.ChunksReused++
			storageIDs = append(storageIDs, sid)
			continue
		}

		sid, found, err := e.idx.GetStorageID(cid)
		if err != nil {
			return nil, pending, err
		}
		if found {
			result.ChunksReused++
			cache[cid] = sid
			storageIDs = append(storageIDs, sid)
			continue
		}

		sid = e.km.DeriveStorageID(cid)
		chunkKey := e.km.DeriveChunkKey(cid)

		compressed := e.compressor.EncodeAll(piece.Data, nil)
		ciphertext, nonce, err := backupcrypto.EncryptChunk(compressed, chunkKey, sid)
		if err != nil {
			return nil, pending, err
		}

		if err := builder.Append(sid, nonce, ciphertext); err != nil {
			return nil, pending, err
		}

		pending = append(pending, pendingChunk{CID: cid, StorageID: sid})
		result.ChunksNew++
		cache[cid] = sid
		storageIDs = append(storageIDs, sid)
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, pending, err
	}

	node := &manifest.FileNode{
		Path:         relPath,
		Size:         int64(len(data)),
		Mode:         uint32(fileInfo.Mode().Perm()),
		ModTimeEpoch: fileInfo.ModTime().Unix(),
		StorageIDs:   storageIDs,
		ContentHash:  backupcrypto.ComputeCID(data),
	}
	return node, pending, nil
}

func (e *Engine) flushPack(ctx context.Context, builder *pack.PackfileBuilder, pending []pendingChunk) error {
	return e.flushPackDetailed(ctx, builder, pending, nil)
}

func (e *Engine) flushPackDetailed(ctx context.Context, builder *pack.PackfileBuilder, pending []pendingChunk, result *BackupResult) error {
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

	if err := e.storage.PutPack(ctx, packID, bytes.NewReader(packBytes), int64(len(packBytes))); err != nil {
		return err
	}
	if result != nil {
		result.PacksWritten++
		result.StoredBytes += int64(len(packBytes))
	}

	now := time.Now().Unix()
	for i, entry := range entries {
		meta := pending[i]
		if err := e.idx.PutCID(meta.CID, meta.StorageID); err != nil {
			return err
		}
		if err := e.idx.PutChunkLocation(meta.StorageID, index.ChunkLocation{
			PackID:         packID,
			Offset:         entry.Offset,
			Length:         entry.Length,
			UploadTimeUnix: now,
		}); err != nil {
			return err
		}
	}

	return nil
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

	if err := os.Chtimes(outPath, time.Unix(fileNode.ModTimeEpoch, 0), time.Unix(fileNode.ModTimeEpoch, 0)); err != nil {
		return err
	}

	return nil
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
	info, err := os.Stat(srcPath)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		return []string{srcPath}, nil
	}

	files := make([]string, 0)
	err = filepath.WalkDir(srcPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(files)
	return files, nil
}
