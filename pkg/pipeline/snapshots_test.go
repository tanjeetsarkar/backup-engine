package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/retention"
)

func TestSnapshotDetailsAndLifecycleRoundTrip(t *testing.T) {
	engine, snapshotID := createManagedSnapshot(t)
	defer engine.Close()

	detail, err := engine.GetSnapshotDetails(snapshotID)
	if err != nil {
		t.Fatalf("GetSnapshotDetails: %v", err)
	}
	if !detail.Readable || detail.Timestamp.IsZero() || detail.TotalFiles != 1 || detail.Lifecycle.State != SnapshotActive {
		t.Fatalf("unexpected legacy defaults/details: %+v", detail)
	}

	retainUntil := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	if _, err := engine.SetSnapshotPin(snapshotID, true); err != nil {
		t.Fatalf("SetSnapshotPin: %v", err)
	}
	if _, err := engine.SetSnapshotRetainUntil(snapshotID, &retainUntil); err != nil {
		t.Fatalf("SetSnapshotRetainUntil: %v", err)
	}
	if _, err := engine.UpdateSnapshotMetadata(snapshotID, SnapshotMetadataUpdate{Labels: []string{"Important", "important", "Photos"}, Note: "offsite candidate"}); err != nil {
		t.Fatalf("UpdateSnapshotMetadata: %v", err)
	}

	raw, found, err := engine.idx.GetSnapshotLifecycle(snapshotID)
	if err != nil || !found {
		t.Fatalf("GetSnapshotLifecycle: found=%v err=%v", found, err)
	}
	if bytes.Contains(raw, []byte("offsite candidate")) || bytes.Contains(raw, []byte("Important")) {
		t.Fatal("lifecycle labels or notes were stored in plaintext")
	}

	lifecycle, err := engine.TrashSnapshot(snapshotID, time.Hour)
	if err != nil {
		t.Fatalf("TrashSnapshot: %v", err)
	}
	if lifecycle.State != SnapshotTrashed || lifecycle.TrashedAt == nil || lifecycle.PurgeAfter == nil || !lifecycle.Pinned {
		t.Fatalf("unexpected trashed lifecycle: %+v", lifecycle)
	}
	active, err := engine.ListSnapshotDetails(false)
	if err != nil || len(active) != 0 {
		t.Fatalf("active details after trash: len=%d err=%v", len(active), err)
	}

	lifecycle, err = engine.UntrashSnapshot(snapshotID)
	if err != nil {
		t.Fatalf("UntrashSnapshot: %v", err)
	}
	if lifecycle.State != SnapshotActive || lifecycle.TrashedAt != nil || lifecycle.PurgeAfter != nil || len(lifecycle.Labels) != 2 || lifecycle.Note == "" || lifecycle.RetainUntil == nil {
		t.Fatalf("unexpected restored lifecycle: %+v", lifecycle)
	}
}

func TestListSnapshotDetailsNewestFirst(t *testing.T) {
	engine, firstID := createManagedSnapshot(t)
	defer engine.Close()
	time.Sleep(2 * time.Millisecond)

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "second.txt"), []byte("second"), 0640); err != nil {
		t.Fatal(err)
	}
	secondID, err := engine.BackupPath(context.Background(), source, &firstID, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath second: %v", err)
	}
	details, err := engine.ListSnapshotDetails(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 2 || details[0].ID != secondID || !details[0].Timestamp.After(details[1].Timestamp) {
		t.Fatalf("unexpected snapshot order: %+v", details)
	}
}

func TestGCLifecyclePrecedenceAndExpiredTrashRemoval(t *testing.T) {
	engine, snapshotID := createManagedSnapshot(t)
	defer engine.Close()

	if _, err := engine.TrashSnapshot(snapshotID, time.Hour); err != nil {
		t.Fatalf("TrashSnapshot: %v", err)
	}
	result, err := engine.RunGCDetailed(context.Background(), retention.GFSPolicy{}, 0, nil)
	if err != nil {
		t.Fatalf("RunGCDetailed recoverable trash: %v", err)
	}
	if result.SnapshotsRetained != 1 || result.Decisions[0].Reason != "Recoverable trash" {
		t.Fatalf("recoverable trash was not retained: %+v", result)
	}
	if _, found, err := engine.idx.GetSnapshot(snapshotID); err != nil || !found {
		t.Fatalf("recoverable snapshot missing: found=%v err=%v", found, err)
	}

	lifecycle, err := engine.readSnapshotLifecycle(snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	lifecycle.PurgeAfter = &past
	if err := engine.writeSnapshotLifecycle(snapshotID, lifecycle); err != nil {
		t.Fatal(err)
	}
	result, err = engine.RunGCDetailed(context.Background(), retention.GFSPolicy{}, 0, nil)
	if err != nil {
		t.Fatalf("RunGCDetailed expired trash: %v", err)
	}
	if result.SnapshotsDropped != 1 || result.Decisions[0].Reason != "Trash recovery window expired" {
		t.Fatalf("expired trash was not dropped: %+v", result)
	}
	if _, found, err := engine.idx.GetSnapshot(snapshotID); err != nil || found {
		t.Fatalf("expired snapshot still listed: found=%v err=%v", found, err)
	}
}

func TestPinnedSnapshotOverridesGFSExpiration(t *testing.T) {
	engine, snapshotID := createManagedSnapshot(t)
	defer engine.Close()
	if _, err := engine.SetSnapshotPin(snapshotID, true); err != nil {
		t.Fatal(err)
	}
	result, err := engine.RunGCDetailed(context.Background(), retention.GFSPolicy{}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.SnapshotsRetained != 1 || result.Decisions[0].Reason != "Pinned" || result.ChunksPurged != 0 {
		t.Fatalf("pinned snapshot was not protected: %+v", result)
	}
}

func createManagedSnapshot(t *testing.T) (*Engine, [32]byte) {
	t.Helper()
	repo := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("managed snapshot"), 0640); err != nil {
		t.Fatal(err)
	}
	engine, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	snapshotID, err := engine.BackupPath(context.Background(), source, nil, []string{"DAILY"})
	if err != nil {
		engine.Close()
		t.Fatal(err)
	}
	return engine, snapshotID
}
