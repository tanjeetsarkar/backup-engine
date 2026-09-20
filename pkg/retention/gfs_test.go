package retention

import (
	"context"
	"testing"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
)

func TestEvaluateGFSDailyOnly(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	manifests := make([]*manifest.SnapshotManifest, 0, 5)
	for i := 0; i < 5; i++ {
		manifests = append(manifests, &manifest.SnapshotManifest{
			Timestamp: now.Add(-time.Duration(i) * 24 * time.Hour),
		})
	}

	results := EvaluateGFS(manifests, GFSPolicy{
		KeepDaily:   2,
		KeepWeekly:  0,
		KeepMonthly: 0,
		KeepYearly:  0,
	})

	kept := 0
	for _, r := range results {
		if r.Keep {
			kept++
			if r.Reason != "GFS: Daily (Son)" {
				t.Fatalf("expected daily reason, got %q", r.Reason)
			}
		}
	}

	if kept != 2 {
		t.Fatalf("expected 2 kept manifests, got %d", kept)
	}
}

func TestGarbageCollectorRunGracePeriod(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	gc := NewGarbageCollectorWithClock(nil, nil, 24*time.Hour, func() time.Time { return now })

	var liveSID [32]byte
	liveSID[0] = 1

	retained := []*manifest.SnapshotManifest{
		{
			Root: &manifest.DirectoryNode{
				Path: "/",
				Files: []*manifest.FileNode{
					{StorageIDs: [][32]byte{liveSID}},
				},
			},
		},
	}

	var packID [32]byte
	packID[0] = 9

	var deadOldSID [32]byte
	deadOldSID[0] = 2
	var deadRecentSID [32]byte
	deadRecentSID[0] = 3

	chunks := []ChunkLocation{
		{
			PackID:         packID,
			StorageID:      liveSID,
			Length:         10,
			UploadTimeUnix: now.Add(-48 * time.Hour).Unix(),
		},
		{
			PackID:         packID,
			StorageID:      deadOldSID,
			Length:         10,
			UploadTimeUnix: now.Add(-48 * time.Hour).Unix(),
		},
		{
			PackID:         packID,
			StorageID:      deadRecentSID,
			Length:         10,
			UploadTimeUnix: now.Add(-6 * time.Hour).Unix(),
		},
	}

	purged, err := gc.Run(context.Background(), retained, chunks)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if purged != 1 {
		t.Fatalf("expected 1 purged chunk, got %d", purged)
	}
}
