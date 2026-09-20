package manifest

import (
	"testing"

	"github.com/tanjeetsarkar/backup-engine/pkg/crypto"
)

func TestSnapshotEnvelopeRoundTrip(t *testing.T) {
	km, err := crypto.NewKeyManager([]byte("passphrase"), []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewKeyManager: %v", err)
	}

	snapshot := &SnapshotManifest{
		RetentionTags: []string{"DAILY"},
		Root: &DirectoryNode{
			Path: "/",
			Files: []*FileNode{
				{Path: "/a.txt", Size: 3},
			},
		},
	}

	env, err := EncryptSnapshotEnvelope(snapshot, km)
	if err != nil {
		t.Fatalf("EncryptSnapshotEnvelope: %v", err)
	}

	decoded, err := DecryptSnapshotEnvelope(env, km)
	if err != nil {
		t.Fatalf("DecryptSnapshotEnvelope: %v", err)
	}

	if decoded.Root == nil || decoded.Root.Path != "/" {
		t.Fatalf("unexpected decoded root")
	}
	if len(decoded.RetentionTags) != 1 || decoded.RetentionTags[0] != "DAILY" {
		t.Fatalf("retention tags mismatch")
	}
}
