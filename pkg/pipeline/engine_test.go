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
