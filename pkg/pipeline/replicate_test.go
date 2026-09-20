package pipeline

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestManifestSourceAdapterExposesSnapshotEnvelopesAsPacks(t *testing.T) {
	engine, snapshotID := createManagedSnapshot(t)
	defer engine.Close()

	adapter := newManifestSourceAdapter(engine.idx)

	ids, err := adapter.ListPacks(context.Background())
	if err != nil {
		t.Fatalf("ListPacks: %v", err)
	}
	if len(ids) != 1 || ids[0] != snapshotID {
		t.Fatalf("expected exactly the one committed snapshot, got %+v", ids)
	}

	rc, err := adapter.GetPack(context.Background(), snapshotID)
	if err != nil {
		t.Fatalf("GetPack: %v", err)
	}
	defer rc.Close()
	envelope, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	direct, found, err := engine.idx.GetSnapshot(snapshotID)
	if err != nil || !found {
		t.Fatalf("GetSnapshot: found=%v err=%v", found, err)
	}
	if !bytes.Equal(envelope, direct) {
		t.Fatalf("adapter envelope does not match index-stored envelope")
	}

	var missing [32]byte
	missing[0] = 0xEE
	if _, err := adapter.GetPack(context.Background(), missing); err == nil {
		t.Fatal("expected error for unknown snapshot id")
	}

	if err := adapter.PutPack(context.Background(), snapshotID, bytes.NewReader(nil), 0); err == nil {
		t.Fatal("expected manifest source adapter to reject writes")
	}
	if err := adapter.DeletePack(context.Background(), snapshotID); err == nil {
		t.Fatal("expected manifest source adapter to reject deletes")
	}
}

func TestReplicateDetailedRejectsLocalRemoteBackend(t *testing.T) {
	engine, _ := createManagedSnapshot(t)
	defer engine.Close()

	if _, err := engine.ReplicateDetailed(context.Background(), StorageConfig{Backend: StorageBackendLocal}, nil); err == nil {
		t.Fatal("expected error when replication remote backend is local")
	}
	if _, err := engine.ReplicateDetailed(context.Background(), StorageConfig{}, nil); err == nil {
		t.Fatal("expected error when replication remote backend is unset (defaults to local)")
	}
}

func TestWithSubPrefixJoinsCleanly(t *testing.T) {
	cases := []struct {
		base string
		sub  string
		want string
	}{
		{"", "packs", "packs"},
		{"root", "packs", "root/packs"},
		{"root/nested", "manifests", "root/nested/manifests"},
	}
	for _, tc := range cases {
		got := withSubPrefix(StorageConfig{Prefix: tc.base}, tc.sub).Prefix
		if got != tc.want {
			t.Fatalf("withSubPrefix(%q, %q) = %q, want %q", tc.base, tc.sub, got, tc.want)
		}
	}
}
