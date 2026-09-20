package manifest

import (
	"encoding/json"
	"fmt"

	"github.com/tanjeetsarkar/backup-engine/pkg/crypto"
)

// SnapshotEnvelope carries encrypted snapshot payload and nonce.
type SnapshotEnvelope struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// MarshalSnapshot serializes a snapshot manifest.
func MarshalSnapshot(snapshot *SnapshotManifest) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot cannot be nil")
	}
	return json.Marshal(snapshot)
}

// UnmarshalSnapshot deserializes a snapshot manifest payload.
func UnmarshalSnapshot(raw []byte) (*SnapshotManifest, error) {
	var snapshot SnapshotManifest
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return &snapshot, nil
}

// EncryptSnapshotEnvelope serializes and encrypts snapshot metadata.
func EncryptSnapshotEnvelope(snapshot *SnapshotManifest, km *crypto.KeyManager) ([]byte, error) {
	if km == nil {
		return nil, fmt.Errorf("key manager cannot be nil")
	}

	plain, err := MarshalSnapshot(snapshot)
	if err != nil {
		return nil, err
	}

	ciphertext, nonce, err := km.EncryptMetadata(plain)
	if err != nil {
		return nil, fmt.Errorf("encrypt snapshot metadata: %w", err)
	}

	env := SnapshotEnvelope{Nonce: nonce, Ciphertext: ciphertext}
	encoded, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot envelope: %w", err)
	}

	return encoded, nil
}

// DecryptSnapshotEnvelope decrypts and deserializes snapshot metadata.
func DecryptSnapshotEnvelope(raw []byte, km *crypto.KeyManager) (*SnapshotManifest, error) {
	if km == nil {
		return nil, fmt.Errorf("key manager cannot be nil")
	}

	var env SnapshotEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot envelope: %w", err)
	}

	plain, err := km.DecryptMetadata(env.Ciphertext, env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decrypt snapshot metadata: %w", err)
	}

	return UnmarshalSnapshot(plain)
}
