package pipeline

import "time"

// PermissionPolicy controls whether unreadable source entries fail or are omitted from a backup.
type PermissionPolicy string

const (
	PermissionPolicyFail PermissionPolicy = "fail"
	PermissionPolicySkip PermissionPolicy = "skip"
)

// BackupOptions configures source handling and parallelism for a backup.
type BackupOptions struct {
	PermissionPolicy PermissionPolicy
	Workers          int
}

// SkippedItem describes a source entry omitted from a backup.
type SkippedItem struct {
	Path      string
	Kind      string
	Operation string
	Reason    string
}

// BackupScanResult describes readable files and source entries that could not be read.
type BackupScanResult struct {
	Files        []string
	SkippedItems []SkippedItem
}

// Operation identifies a user-facing repository workflow.
type Operation string

const (
	OperationBackup     Operation = "backup"
	OperationRestore    Operation = "restore"
	OperationVerify     Operation = "verify"
	OperationDoctor     Operation = "doctor"
	OperationGC         Operation = "gc"
	OperationInit       Operation = "init"
	OperationHardDelete Operation = "snapshot-remove"
	OperationReplicate  Operation = "replicate"
	OperationScrub      Operation = "scrub"
	OperationRecover    Operation = "recover"
)

// Phase identifies the current stage of an operation.
type Phase string

const (
	PhasePreparing  Phase = "preparing"
	PhaseScanning   Phase = "scanning"
	PhaseProcessing Phase = "processing"
	PhaseWriting    Phase = "writing"
	PhaseCommitting Phase = "committing"
	PhaseReading    Phase = "reading"
	PhaseChecking   Phase = "checking"
	PhasePlanning   Phase = "planning"
	PhaseCompacting Phase = "compacting"
	PhaseComplete   Phase = "complete"
)

// EventLevel describes the importance of an operation event.
type EventLevel string

const (
	EventInfo    EventLevel = "info"
	EventSuccess EventLevel = "success"
	EventWarning EventLevel = "warning"
)

// ProgressEvent is a presentation-neutral operation update.
type ProgressEvent struct {
	Operation Operation
	Phase     Phase
	Level     EventLevel
	Message   string
	Completed int64
	Total     int64
	Unit      string
	Time      time.Time
}

// Reporter receives progress events synchronously. Implementations should return quickly.
type Reporter func(ProgressEvent)

func emit(reporter Reporter, event ProgressEvent) {
	if reporter == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	reporter(event)
}

// BackupResult summarizes a completed or partially completed backup.
type BackupResult struct {
	SnapshotID     [32]byte
	FilesScanned   int64
	FilesProcessed int64
	FilesSkipped   int64
	SkippedBytes   int64
	SkippedItems   []SkippedItem
	Workers        int
	LogicalBytes   int64
	ChunksExamined int64
	ChunksNew      int64
	ChunksReused   int64
	PacksWritten   int64
	StoredBytes    int64
	RetentionTags  []string
	Duration       time.Duration
}

// RestoreResult summarizes a completed or partially completed restore.
type RestoreResult struct {
	SnapshotID    [32]byte
	Destination   string
	FilesRestored int64
	LogicalBytes  int64
	ChunksRead    int64
	Duration      time.Duration
}

// InitResult summarizes repository initialization.
type InitResult struct {
	Repository    string
	BoundExisting bool
	KeyCheckReady bool
	Duration      time.Duration
}

// VerifyResult summarizes repository verification.
type VerifyResult struct {
	SnapshotsChecked int64
	FilesChecked     int64
	ChunksChecked    int64
	LogicalBytes     int64
	Duration         time.Duration
}

// DoctorIssue is a structured repository consistency problem.
type DoctorIssue struct {
	Code     string
	Severity string
	Resource string
	Summary  string
	Detail   string
	Hint     string
}

// DoctorResult summarizes repository diagnostics.
type DoctorResult struct {
	ChunkRecordsChecked int64
	Issues              []DoctorIssue
	Verify              VerifyResult
	Duration            time.Duration
}

// ScrubResult summarizes a bit-rot scan of every stored packfile.
type ScrubResult struct {
	PacksScanned int64
	PacksCorrupt int64
	Issues       []DoctorIssue
	Duration     time.Duration
}

// RecoverResult summarizes rebuilding a local repository from remote pack and manifest storage.
// It does not rebuild the CID/dedup index: convergent chunk encryption keys are themselves derived
// from the CID (see pkg/crypto DeriveChunkKey), so a chunk cannot be decrypted, and therefore its
// CID cannot be recomputed, without already knowing the CID. Deduplication efficiency is restored
// gradually as new backups touch the same content again (see README "Disaster Recovery").
type RecoverResult struct {
	ManifestsRecovered int64
	PacksScanned       int64
	ChunkLocations     int64
	// CIDDirectoryFound reports whether the remote had a replicated CID directory to restore from.
	// If false, restore/verify will fail for every chunk until a full rebuild from original
	// sources, since chunk decryption keys are derived from the CID (see pkg/crypto.DeriveChunkKey)
	// and it cannot be recovered any other way.
	CIDDirectoryFound bool
	CIDsRecovered     int64
	Verify            VerifyResult
	Duration          time.Duration
}

// RetentionDecision explains why a snapshot is retained or released.
type RetentionDecision struct {
	SnapshotID [32]byte
	Keep       bool
	Reason     string
}

// GCResult summarizes retention evaluation and garbage collection.
type GCResult struct {
	SnapshotsEvaluated int64
	SnapshotsRetained  int64
	SnapshotsDropped   int64
	ChunksExamined     int64
	ChunksPurged       int64
	Decisions          []RetentionDecision
	Duration           time.Duration
}

// HardDeleteResult summarizes an instant, trash-bypassing snapshot removal.
type HardDeleteResult struct {
	SnapshotID      [32]byte
	ChunksReclaimed int64
	BytesReclaimed  int64
	Duration        time.Duration
}

// ReplicationCounts summarizes one category (packs or manifests) of a replication pass.
type ReplicationCounts struct {
	ItemsReplicated int64
	ItemsSkipped    int64
	BytesReplicated int64
}

// ReplicationResult summarizes a full local-to-remote replication pass covering both packfiles
// and snapshot manifests.
type ReplicationResult struct {
	Packs     ReplicationCounts
	Manifests ReplicationCounts
	// CIDsReplicated is the number of CID<->StorageID mappings included in the encrypted CID
	// directory snapshot pushed to the remote (required for disaster recovery; see Engine.Recover).
	CIDsReplicated int64
	Duration       time.Duration
}
