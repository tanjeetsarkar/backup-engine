package manifest

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/crypto"
)

func TestMarshalSnapshotWireRoundTripWithExtendedMetadata(t *testing.T) {
	parent := [32]byte{9}
	var storageID [32]byte
	storageID[0] = 1
	snapshot := &SnapshotManifest{
		SnapshotID:       [32]byte{1},
		ParentSnapshotID: &parent,
		Timestamp:        time.Date(2026, 9, 20, 12, 0, 0, 123456789, time.UTC),
		RetentionTags:    []string{"DAILY", "WEEKLY"},
		Root: &DirectoryNode{
			Path: "/",
			Subdirs: []*DirectoryNode{
				{Path: "/sub", Files: []*FileNode{
					{Path: "/sub/nested.txt", Size: 5, StorageIDs: [][32]byte{storageID}},
				}},
			},
			Files: []*FileNode{
				{
					Path:         "/a.txt",
					Size:         3,
					Mode:         0640,
					ModTimeEpoch: 1700000000,
					StorageIDs:   [][32]byte{storageID},
					UID:          1000,
					GID:          1000,
					XAttrs: []XAttr{
						{Name: "user.comment", Value: []byte("hello")},
						{Name: "system.posix_acl_access", Value: []byte{0x02, 0x00}},
					},
				},
				{
					Path:          "/link",
					SymlinkTarget: "a.txt",
				},
			},
		},
		MerkleRoot: [32]byte{2},
		TotalBytes: 8,
		TotalFiles: 3,
	}

	raw, err := MarshalSnapshotWire(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshotWire: %v", err)
	}
	decoded, err := UnmarshalSnapshotWire(raw)
	if err != nil {
		t.Fatalf("UnmarshalSnapshotWire: %v", err)
	}

	if decoded.SnapshotID != snapshot.SnapshotID || decoded.ParentSnapshotID == nil || *decoded.ParentSnapshotID != parent {
		t.Fatalf("snapshot/parent id mismatch: %+v", decoded)
	}
	if !decoded.Timestamp.Equal(snapshot.Timestamp) {
		t.Fatalf("timestamp mismatch: got %v want %v", decoded.Timestamp, snapshot.Timestamp)
	}
	if len(decoded.RetentionTags) != 2 || decoded.RetentionTags[0] != "DAILY" || decoded.RetentionTags[1] != "WEEKLY" {
		t.Fatalf("retention tags mismatch: %+v", decoded.RetentionTags)
	}
	if decoded.MerkleRoot != snapshot.MerkleRoot || decoded.TotalBytes != 8 || decoded.TotalFiles != 3 {
		t.Fatalf("summary fields mismatch: %+v", decoded)
	}
	if decoded.Root == nil || len(decoded.Root.Subdirs) != 1 || decoded.Root.Subdirs[0].Path != "/sub" {
		t.Fatalf("nested directory not preserved: %+v", decoded.Root)
	}
	if len(decoded.Root.Subdirs[0].Files) != 1 || decoded.Root.Subdirs[0].Files[0].Path != "/sub/nested.txt" {
		t.Fatalf("nested file not preserved: %+v", decoded.Root.Subdirs[0].Files)
	}
	if len(decoded.Root.Files) != 2 {
		t.Fatalf("expected 2 root files, got %d", len(decoded.Root.Files))
	}
	regular, link := decoded.Root.Files[0], decoded.Root.Files[1]
	if regular.UID != 1000 || regular.GID != 1000 || regular.Mode != 0640 {
		t.Fatalf("uid/gid/mode not preserved: %+v", regular)
	}
	if len(regular.XAttrs) != 2 || regular.XAttrs[0].Name != "user.comment" || string(regular.XAttrs[0].Value) != "hello" {
		t.Fatalf("xattrs not preserved: %+v", regular.XAttrs)
	}
	if regular.XAttrs[1].Name != "system.posix_acl_access" {
		t.Fatalf("acl-as-xattr not preserved: %+v", regular.XAttrs)
	}
	if !link.IsSymlink() || link.SymlinkTarget != "a.txt" {
		t.Fatalf("symlink target not preserved: %+v", link)
	}
}

func TestDecryptSnapshotEnvelopeReadsLegacyJSONFormat(t *testing.T) {
	km, err := crypto.NewKeyManager([]byte("passphrase"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager: %v", err)
	}
	snapshot := &SnapshotManifest{
		RetentionTags: []string{"DAILY"},
		Root:          &DirectoryNode{Path: "/", Files: []*FileNode{{Path: "/a.txt", Size: 3}}},
	}

	plain, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot (legacy JSON): %v", err)
	}
	ciphertext, nonce, err := km.EncryptMetadata(plain)
	if err != nil {
		t.Fatalf("EncryptMetadata: %v", err)
	}
	// Simulate an envelope written by a pre-migration repository: no "format" field at all.
	legacyEnvelope, err := json.Marshal(struct {
		Nonce      []byte `json:"nonce"`
		Ciphertext []byte `json:"ciphertext"`
	}{Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		t.Fatalf("marshal legacy envelope: %v", err)
	}

	decoded, err := DecryptSnapshotEnvelope(legacyEnvelope, km)
	if err != nil {
		t.Fatalf("DecryptSnapshotEnvelope (legacy): %v", err)
	}
	if decoded.Root == nil || decoded.Root.Path != "/" || len(decoded.RetentionTags) != 1 {
		t.Fatalf("legacy snapshot not decoded correctly: %+v", decoded)
	}
}

func TestEncryptSnapshotEnvelopeWritesProtobufV2Format(t *testing.T) {
	km, err := crypto.NewKeyManager([]byte("passphrase"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager: %v", err)
	}
	snapshot := &SnapshotManifest{Root: &DirectoryNode{Path: "/"}}

	env, err := EncryptSnapshotEnvelope(snapshot, km)
	if err != nil {
		t.Fatalf("EncryptSnapshotEnvelope: %v", err)
	}
	if !bytes.Contains(env, []byte(envelopeFormatProtobufV2)) {
		t.Fatalf("expected new envelopes to declare format %q", envelopeFormatProtobufV2)
	}
}

func TestDecryptSnapshotEnvelopeRejectsUnknownFormat(t *testing.T) {
	km, err := crypto.NewKeyManager([]byte("passphrase"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager: %v", err)
	}
	ciphertext, nonce, err := km.EncryptMetadata([]byte("irrelevant"))
	if err != nil {
		t.Fatalf("EncryptMetadata: %v", err)
	}
	raw, err := json.Marshal(SnapshotEnvelope{Format: "future-v99", Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if _, err := DecryptSnapshotEnvelope(raw, km); err == nil {
		t.Fatal("expected error for unknown envelope format")
	}
}
