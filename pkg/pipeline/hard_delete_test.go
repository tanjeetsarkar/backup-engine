package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHardDeleteSnapshotReclaimsUniqueChunksKeepsShared(t *testing.T) {
	engine, snapshotOneID := createManagedSnapshot(t)
	defer engine.Close()

	sourceTwo := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceTwo, "file.txt"), []byte("managed snapshot"), 0640); err != nil {
		t.Fatal(err)
	}
	snapshotTwoID, err := engine.BackupPath(context.Background(), sourceTwo, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath second: %v", err)
	}

	before, err := engine.idx.ListChunkRecords()
	if err != nil {
		t.Fatalf("ListChunkRecords before: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected the two snapshots to share exactly one deduplicated chunk, got %d", len(before))
	}

	sourceThree := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceThree, "unique.txt"), []byte("only in snapshot three, not shared with anything else"), 0640); err != nil {
		t.Fatal(err)
	}
	snapshotThreeID, err := engine.BackupPath(context.Background(), sourceThree, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath third: %v", err)
	}

	result, err := engine.HardDeleteSnapshotDetailed(context.Background(), snapshotThreeID, nil)
	if err != nil {
		t.Fatalf("HardDeleteSnapshotDetailed: %v", err)
	}
	if result.ChunksReclaimed != 1 {
		t.Fatalf("expected 1 reclaimed chunk, got %d", result.ChunksReclaimed)
	}
	if result.BytesReclaimed == 0 {
		t.Fatalf("expected non-zero bytes reclaimed")
	}

	if _, found, err := engine.idx.GetSnapshot(snapshotThreeID); err != nil || found {
		t.Fatalf("expected removed snapshot to be gone: found=%v err=%v", found, err)
	}

	after, err := engine.idx.ListChunkRecords()
	if err != nil {
		t.Fatalf("ListChunkRecords after: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("expected the shared chunk to survive hard delete, got %d chunk records", len(after))
	}

	// Both untouched snapshots must still restore cleanly.
	for _, id := range []struct {
		snapshotID [32]byte
		name       string
	}{{snapshotOneID, "one"}, {snapshotTwoID, "two"}} {
		dest := t.TempDir()
		if _, err := engine.RestoreSnapshotDetailed(context.Background(), id.snapshotID, dest, nil); err != nil {
			t.Fatalf("restore snapshot %s after hard delete: %v", id.name, err)
		}
		data, err := os.ReadFile(filepath.Join(dest, "file.txt"))
		if err != nil || string(data) != "managed snapshot" {
			t.Fatalf("unexpected restored content for snapshot %s: data=%q err=%v", id.name, data, err)
		}
	}
}

func TestHardDeleteSnapshotNotFound(t *testing.T) {
	engine, _ := createManagedSnapshot(t)
	defer engine.Close()

	var missing [32]byte
	missing[0] = 0xFF
	if _, err := engine.HardDeleteSnapshotDetailed(context.Background(), missing, nil); err == nil {
		t.Fatal("expected error for missing snapshot")
	}
}
