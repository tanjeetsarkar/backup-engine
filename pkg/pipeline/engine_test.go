package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/retention"
)

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()
	restoreDir := t.TempDir()

	srcFile := filepath.Join(sourceDir, "nested", "hello.txt")
	if err := os.MkdirAll(filepath.Dir(srcFile), 0750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	original := []byte("hello backup engine\nthis is a test payload")
	if err := os.WriteFile(srcFile, original, 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eng, err := Open(EngineConfig{
		RepoDir:    repo,
		Passphrase: []byte("passphrase"),
		Salt:       []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	snapshotID, err := eng.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath: %v", err)
	}

	if err := eng.RestoreSnapshot(context.Background(), snapshotID, restoreDir); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	restoredFile := filepath.Join(restoreDir, "nested", "hello.txt")
	restored, err := os.ReadFile(restoredFile)
	if err != nil {
		t.Fatalf("ReadFile restored: %v", err)
	}

	if string(restored) != string(original) {
		t.Fatalf("restored content mismatch")
	}
}

func TestBackupDeduplicatesAcrossSnapshots(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()

	srcFile := filepath.Join(sourceDir, "same.txt")
	content := []byte("the same content should deduplicate")
	if err := os.WriteFile(srcFile, content, 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eng, err := Open(EngineConfig{
		RepoDir:        repo,
		Passphrase:     []byte("passphrase"),
		Salt:           []byte("0123456789abcdef"),
		PackTargetSize: 1024,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	_, err = eng.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath first: %v", err)
	}

	recordsBefore, err := eng.idx.ListChunkRecords()
	if err != nil {
		t.Fatalf("ListChunkRecords first: %v", err)
	}

	_, err = eng.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath second: %v", err)
	}

	recordsAfter, err := eng.idx.ListChunkRecords()
	if err != nil {
		t.Fatalf("ListChunkRecords second: %v", err)
	}

	if len(recordsAfter) != len(recordsBefore) {
		t.Fatalf("expected dedup to prevent new chunk records; before=%d after=%d", len(recordsBefore), len(recordsAfter))
	}

	snapshots, err := eng.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected two snapshots, got %d", len(snapshots))
	}
}

func TestBackupDetailedReportsProgressAndDedupMetrics(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()
	content := []byte("repeatable content for detailed backup accounting")
	if err := os.WriteFile(filepath.Join(sourceDir, "data.txt"), content, 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eng, err := Open(EngineConfig{
		RepoDir:    repo,
		Passphrase: []byte("passphrase"),
		Salt:       []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	events := make([]ProgressEvent, 0)
	first, err := eng.BackupPathDetailed(context.Background(), sourceDir, nil, []string{"DAILY"}, func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("BackupPathDetailed first: %v", err)
	}
	if first.FilesScanned != 1 || first.FilesProcessed != 1 || first.LogicalBytes != int64(len(content)) {
		t.Fatalf("unexpected file metrics: %+v", first)
	}
	if first.ChunksExamined == 0 || first.ChunksNew != first.ChunksExamined || first.ChunksReused != 0 {
		t.Fatalf("unexpected first chunk metrics: %+v", first)
	}
	if first.PacksWritten == 0 || first.StoredBytes == 0 || first.Duration <= 0 {
		t.Fatalf("unexpected storage metrics: %+v", first)
	}
	if len(events) < 4 || events[0].Phase != PhaseScanning || events[len(events)-1].Phase != PhaseComplete {
		t.Fatalf("unexpected progress events: %+v", events)
	}

	second, err := eng.BackupPathDetailed(context.Background(), sourceDir, nil, []string{"DAILY"}, nil)
	if err != nil {
		t.Fatalf("BackupPathDetailed second: %v", err)
	}
	if second.ChunksExamined == 0 || second.ChunksReused != second.ChunksExamined || second.ChunksNew != 0 {
		t.Fatalf("unexpected duplicate chunk metrics: %+v", second)
	}
	if second.PacksWritten != 0 || second.StoredBytes != 0 {
		t.Fatalf("duplicate backup wrote new storage: %+v", second)
	}
}

func TestVerifyAndDoctorHealthyRepository(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()

	srcFile := filepath.Join(sourceDir, "ok.txt")
	if err := os.WriteFile(srcFile, []byte("healthy repo content"), 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eng, err := Open(EngineConfig{
		RepoDir:    repo,
		Passphrase: []byte("passphrase"),
		Salt:       []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	if _, err := eng.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"}); err != nil {
		t.Fatalf("BackupPath: %v", err)
	}

	if err := eng.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if err := eng.Doctor(context.Background()); err != nil {
		t.Fatalf("Doctor: %v", err)
	}
}

func TestDetailedRestoreVerifyAndDoctorMetrics(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()
	restoreDir := t.TempDir()
	content := []byte("detailed operation metrics")
	if err := os.WriteFile(filepath.Join(sourceDir, "metrics.txt"), content, 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eng, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	backup, err := eng.BackupPathDetailed(context.Background(), sourceDir, nil, []string{"DAILY"}, nil)
	if err != nil {
		t.Fatalf("BackupPathDetailed: %v", err)
	}
	restore, err := eng.RestoreSnapshotDetailed(context.Background(), backup.SnapshotID, restoreDir, nil)
	if err != nil {
		t.Fatalf("RestoreSnapshotDetailed: %v", err)
	}
	if restore.FilesRestored != 1 || restore.LogicalBytes != int64(len(content)) || restore.ChunksRead == 0 {
		t.Fatalf("unexpected restore metrics: %+v", restore)
	}

	verify, err := eng.VerifyDetailed(context.Background(), nil)
	if err != nil {
		t.Fatalf("VerifyDetailed: %v", err)
	}
	if verify.SnapshotsChecked != 1 || verify.FilesChecked != 1 || verify.LogicalBytes != int64(len(content)) || verify.ChunksChecked == 0 {
		t.Fatalf("unexpected verify metrics: %+v", verify)
	}

	doctor, err := eng.DoctorDetailed(context.Background(), nil)
	if err != nil {
		t.Fatalf("DoctorDetailed: %v", err)
	}
	if len(doctor.Issues) != 0 || doctor.ChunkRecordsChecked == 0 || doctor.Verify.SnapshotsChecked != 1 {
		t.Fatalf("unexpected doctor metrics: %+v", doctor)
	}
}

func TestRunGCPurgesUnretainedChunks(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()

	srcFile := filepath.Join(sourceDir, "gc.txt")
	if err := os.WriteFile(srcFile, []byte("chunk for gc purge"), 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eng, err := Open(EngineConfig{
		RepoDir:    repo,
		Passphrase: []byte("passphrase"),
		Salt:       []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	if _, err := eng.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"}); err != nil {
		t.Fatalf("BackupPath: %v", err)
	}

	recordsBefore, err := eng.idx.ListChunkRecords()
	if err != nil {
		t.Fatalf("ListChunkRecords before: %v", err)
	}
	if len(recordsBefore) == 0 {
		t.Fatalf("expected at least one chunk record before gc")
	}

	purged, err := eng.RunGC(context.Background(), retention.GFSPolicy{}, 0*time.Second)
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if purged == 0 {
		t.Fatalf("expected at least one purged chunk")
	}

	recordsAfter, err := eng.idx.ListChunkRecords()
	if err != nil {
		t.Fatalf("ListChunkRecords after: %v", err)
	}
	if len(recordsAfter) != 0 {
		t.Fatalf("expected all chunk records to be purged, got %d", len(recordsAfter))
	}
}

func TestRunGCDetailedReportsRetentionDecisions(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "gc-details.txt"), []byte("gc details"), 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eng, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()
	if _, err := eng.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"}); err != nil {
		t.Fatalf("BackupPath: %v", err)
	}

	result, err := eng.RunGCDetailed(context.Background(), retention.GFSPolicy{}, 0, nil)
	if err != nil {
		t.Fatalf("RunGCDetailed: %v", err)
	}
	if result.SnapshotsEvaluated != 1 || result.SnapshotsDropped != 1 || result.SnapshotsRetained != 0 {
		t.Fatalf("unexpected snapshot metrics: %+v", result)
	}
	if len(result.Decisions) != 1 || result.Decisions[0].Keep || result.ChunksExamined == 0 || result.ChunksPurged == 0 {
		t.Fatalf("unexpected GC metrics: %+v", result)
	}
}

func TestOpenFailsWithWrongKeyAfterInit(t *testing.T) {
	repo := t.TempDir()
	if err := InitRepository(InitConfig{
		RepoDir:    repo,
		Passphrase: []byte("right-pass"),
		Salt:       []byte("0123456789abcdef"),
	}); err != nil {
		t.Fatalf("InitRepository: %v", err)
	}

	_, err := Open(EngineConfig{
		RepoDir:    repo,
		Passphrase: []byte("wrong-pass"),
		Salt:       []byte("0123456789abcdef"),
	})
	if !errors.Is(err, ErrRepositoryKeyMismatch) {
		t.Fatalf("expected ErrRepositoryKeyMismatch, got %v", err)
	}
}

func TestInitExistingRepoRequiresBindExisting(t *testing.T) {
	repo := t.TempDir()

	eng, err := Open(EngineConfig{
		RepoDir:    repo,
		Passphrase: []byte("passphrase"),
		Salt:       []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "x.txt"), []byte("abc"), 0640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := eng.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"}); err != nil {
		t.Fatalf("BackupPath: %v", err)
	}
	if err := eng.idx.DeleteMeta(repoKeyCheckMetaKey); err != nil {
		t.Fatalf("delete keycheck: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = InitRepository(InitConfig{
		RepoDir:    repo,
		Passphrase: []byte("passphrase"),
		Salt:       []byte("0123456789abcdef"),
	})
	if !errors.Is(err, ErrRepositoryNeedsInit) {
		t.Fatalf("expected ErrRepositoryNeedsInit, got %v", err)
	}

	err = InitRepository(InitConfig{
		RepoDir:      repo,
		Passphrase:   []byte("passphrase"),
		Salt:         []byte("0123456789abcdef"),
		BindExisting: true,
	})
	if err != nil {
		t.Fatalf("InitRepository bind existing: %v", err)
	}
}
