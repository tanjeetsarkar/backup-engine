package manifest

import (
	"testing"
	"time"
)

func seedSnapshotManifest() *SnapshotManifest {
	var storageID [32]byte
	storageID[0] = 1
	return &SnapshotManifest{
		SnapshotID:    [32]byte{1},
		Timestamp:     time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
		RetentionTags: []string{"DAILY"},
		Root: &DirectoryNode{
			Path: "/",
			Files: []*FileNode{
				{
					Path:       "/a.txt",
					Size:       3,
					StorageIDs: [][32]byte{storageID},
					XAttrs:     []XAttr{{Name: "user.comment", Value: []byte("hello")}},
				},
			},
		},
		MerkleRoot: [32]byte{2},
		TotalBytes: 3,
		TotalFiles: 1,
	}
}

// FuzzUnmarshalSnapshotWire feeds arbitrary bytes at the protobuf-wire manifest parser, which
// reads bytes that originated from storage (local disk or a remote bucket that could be
// corrupted or, if compromised, adversarially crafted).
func FuzzUnmarshalSnapshotWire(f *testing.F) {
	encoded, err := MarshalSnapshotWire(seedSnapshotManifest())
	if err != nil {
		f.Fatalf("seed marshal: %v", err)
	}
	f.Add(encoded)
	f.Add([]byte(""))
	f.Add([]byte{0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = UnmarshalSnapshotWire(data)
	})
}

// FuzzUnmarshalSnapshot feeds arbitrary bytes at the legacy JSON manifest parser.
func FuzzUnmarshalSnapshot(f *testing.F) {
	encoded, err := MarshalSnapshot(seedSnapshotManifest())
	if err != nil {
		f.Fatalf("seed marshal: %v", err)
	}
	f.Add(encoded)
	f.Add([]byte(""))
	f.Add([]byte("{"))
	f.Add([]byte("null"))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = UnmarshalSnapshot(data)
	})
}
