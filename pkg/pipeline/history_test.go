package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupAndRestorePersistTransactionHistory(t *testing.T) {
	repo := t.TempDir()
	source := t.TempDir()
	restoreDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("history content"), 0640); err != nil {
		t.Fatal(err)
	}

	engine, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	snapshotID, err := engine.BackupPath(context.Background(), source, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath: %v", err)
	}
	if err := engine.RestoreSnapshot(context.Background(), snapshotID, restoreDir); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	engine.Close()

	// Reopen to prove history is durable across process restarts, not just in-memory.
	reopened, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	history, err := reopened.ListTransactionHistory(0)
	if err != nil {
		t.Fatalf("ListTransactionHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries (backup + restore), got %d: %+v", len(history), history)
	}
	// Newest first: restore should precede backup.
	if history[0].Operation != OperationRestore || history[0].Status != "success" {
		t.Fatalf("unexpected newest entry: %+v", history[0])
	}
	if history[1].Operation != OperationBackup || history[1].Status != "success" {
		t.Fatalf("unexpected oldest entry: %+v", history[1])
	}
	if len(history[1].Result) == 0 {
		t.Fatalf("expected backup entry to carry a result payload")
	}
}

func TestFailedOperationRecordsFailedTransactionHistory(t *testing.T) {
	engine, _ := createManagedSnapshot(t)
	defer engine.Close()

	var missing [32]byte
	missing[0] = 0xAB
	if err := engine.RestoreSnapshot(context.Background(), missing, t.TempDir()); err == nil {
		t.Fatal("expected restore of missing snapshot to fail")
	}

	history, err := engine.ListTransactionHistory(1)
	if err != nil {
		t.Fatalf("ListTransactionHistory: %v", err)
	}
	if len(history) != 1 || history[0].Status != "failed" || history[0].Error == "" {
		t.Fatalf("expected most recent entry to record the failure: %+v", history)
	}
}

func TestClearTransactionHistoryBeforeCutoff(t *testing.T) {
	engine, _ := createManagedSnapshot(t)
	defer engine.Close()

	before, err := engine.ListTransactionHistory(0)
	if err != nil {
		t.Fatalf("ListTransactionHistory: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("expected at least one history entry from the initial backup")
	}

	removed, err := engine.ClearTransactionHistoryBefore(before[0].CompletedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("ClearTransactionHistoryBefore: %v", err)
	}
	if removed != len(before) {
		t.Fatalf("expected to remove all %d entries, removed %d", len(before), removed)
	}

	after, err := engine.ListTransactionHistory(0)
	if err != nil {
		t.Fatalf("ListTransactionHistory after clear: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected no history entries after clearing, got %d", len(after))
	}
}
