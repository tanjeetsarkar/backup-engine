package manifest

import (
	"encoding/json"
	"fmt"

	"github.com/tanjeetsarkar/backup-engine/pkg/crypto"
)

// SnapshotEnvelope carries encrypted snapshot payload and nonce. Format is stored unencrypted
// alongside the ciphertext (it reveals nothing about snapshot contents) so DecryptSnapshotEnvelope
// can dispatch to the right plaintext decoder. Empty/"json-v1" means legacy JSON plaintext;
// "protobuf-v2" means the newer, more compact protobuf wire encoding used by new backups.
type SnapshotEnvelope struct {
	Format     string `json:"format,omitempty"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

const (
	envelopeFormatJSONV1     = "json-v1"
	envelopeFormatProtobufV2 = "protobuf-v2"
)

// MarshalSnapshot serializes a snapshot manifest using the legacy JSON plaintext format.
func MarshalSnapshot(snapshot *SnapshotManifest) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot cannot be nil")
	}
	return json.Marshal(snapshot)
}

// UnmarshalSnapshot deserializes a legacy JSON-encoded snapshot manifest payload.
func UnmarshalSnapshot(raw []byte) (*SnapshotManifest, error) {
	var snapshot SnapshotManifest
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return &snapshot, nil
}

// EncryptSnapshotEnvelope serializes and encrypts snapshot metadata. New envelopes always use the
// protobuf wire plaintext format; older repositories' JSON envelopes remain readable (see
// DecryptSnapshotEnvelope) but are rewritten to the new format the next time they are saved.
func EncryptSnapshotEnvelope(snapshot *SnapshotManifest, km *crypto.KeyManager) ([]byte, error) {
	if km == nil {
		return nil, fmt.Errorf("key manager cannot be nil")
	}

	plain, err := MarshalSnapshotWire(snapshot)
	if err != nil {
		return nil, err
	}

	ciphertext, nonce, err := km.EncryptMetadata(plain)
	if err != nil {
		return nil, fmt.Errorf("encrypt snapshot metadata: %w", err)
	}

	env := SnapshotEnvelope{Format: envelopeFormatProtobufV2, Nonce: nonce, Ciphertext: ciphertext}
	encoded, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot envelope: %w", err)
	}

	return encoded, nil
}

// DecryptSnapshotEnvelope decrypts and deserializes snapshot metadata, transparently supporting
// both the legacy JSON plaintext format and the newer protobuf wire format.
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

	switch env.Format {
	case envelopeFormatProtobufV2:
		return UnmarshalSnapshotWire(plain)
	case "", envelopeFormatJSONV1:
		return UnmarshalSnapshot(plain)
	default:
		return nil, fmt.Errorf("unsupported snapshot envelope format %q", env.Format)
	}
}
