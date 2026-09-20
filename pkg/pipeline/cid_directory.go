package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/tanjeetsarkar/backup-engine/pkg/index"
	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
	"github.com/zeebo/blake3"
)

// cidDirectoryObjectID is a fixed, content-independent identifier for the single "current CID
// directory" object stored under a repository's remote "index" prefix. Unlike packs (identified by
// the hash of their own bytes) this object is looked up by a stable name and overwritten on every
// replicate run, since only the latest directory is ever needed.
var cidDirectoryObjectID = blake3.Sum256([]byte("backup-engine-cid-directory-object-v1"))

// cidDirectoryEntry is the compact wire form of one index.CIDMapping.
type cidDirectoryEntry struct {
	CID       []byte `json:"c"`
	StorageID []byte `json:"s"`
}

type cidDirectoryPayload struct {
	Version int                 `json:"version"`
	Entries []cidDirectoryEntry `json:"entries"`
}

// The chunk encryption key is derived from the CID (see pkg/crypto.DeriveChunkKey), so a chunk
// cannot be decrypted -- and StorageID -> CID is a one-way HMAC+hash, so the CID cannot be
// recomputed from the StorageID either -- without already knowing the CID. The "cids"/"cid_by_sid"
// index buckets are therefore not just a dedup optimization: losing them makes every chunk
// permanently undecryptable, even with the correct passphrase/salt. To make disaster recovery
// possible, replicate pushes an encrypted snapshot of the full CID directory (StorageID<->CID
// mappings) to the remote, sealed with the metadata key alone (derivable from passphrase/salt with
// no CID dependency), so recover can rebuild it without needing the original source data.

func buildCIDDirectoryEnvelope(idx *index.DB, km interface {
	EncryptMetadata([]byte) ([]byte, []byte, error)
}) ([]byte, int64, error) {
	mappings, err := idx.ListCIDMappings()
	if err != nil {
		return nil, 0, fmt.Errorf("list cid mappings: %w", err)
	}
	payload := cidDirectoryPayload{Version: 1, Entries: make([]cidDirectoryEntry, 0, len(mappings))}
	for _, m := range mappings {
		payload.Entries = append(payload.Entries, cidDirectoryEntry{CID: append([]byte(nil), m.CID[:]...), StorageID: append([]byte(nil), m.StorageID[:]...)})
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal cid directory: %w", err)
	}
	ciphertext, nonce, err := km.EncryptMetadata(plain)
	if err != nil {
		return nil, 0, fmt.Errorf("encrypt cid directory: %w", err)
	}
	env := manifest.SnapshotEnvelope{Nonce: nonce, Ciphertext: ciphertext}
	encoded, err := json.Marshal(env)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal cid directory envelope: %w", err)
	}
	return encoded, int64(len(payload.Entries)), nil
}

// replicateCIDDirectory pushes the current CID directory to remoteIndex, unconditionally
// overwriting any previous version (it is looked up by a fixed ID, not a content hash).
func (e *Engine) replicateCIDDirectory(ctx context.Context, remoteIndex pack.StorageEngine) (int64, error) {
	encoded, count, err := buildCIDDirectoryEnvelope(e.idx, e.km)
	if err != nil {
		return 0, err
	}
	if err := remoteIndex.PutPack(ctx, cidDirectoryObjectID, bytes.NewReader(encoded), int64(len(encoded))); err != nil {
		return 0, fmt.Errorf("upload cid directory: %w", err)
	}
	return count, nil
}

// recoverCIDDirectory fetches and restores the CID directory from remoteIndex, if present. Any
// failure to fetch it (missing object, since a repository replicated before this feature existed,
// or a transient error) is treated as "not found": recovery proceeds without it, at the cost of
// every chunk remaining permanently undecryptable until a full repository rebuild from original
// sources.
func (e *Engine) recoverCIDDirectory(ctx context.Context, remoteIndex pack.StorageEngine) (found bool, restored int64, err error) {
	rc, getErr := remoteIndex.GetPack(ctx, cidDirectoryObjectID)
	if getErr != nil {
		return false, 0, nil
	}
	defer rc.Close()
	encoded, err := io.ReadAll(rc)
	if err != nil {
		return false, 0, fmt.Errorf("read cid directory: %w", err)
	}

	var env manifest.SnapshotEnvelope
	if err := json.Unmarshal(encoded, &env); err != nil {
		return false, 0, fmt.Errorf("decode cid directory envelope: %w", err)
	}
	plain, err := e.km.DecryptMetadata(env.Ciphertext, env.Nonce)
	if err != nil {
		return false, 0, fmt.Errorf("decrypt cid directory: %w", err)
	}
	var payload cidDirectoryPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return false, 0, fmt.Errorf("decode cid directory payload: %w", err)
	}

	for _, entry := range payload.Entries {
		if len(entry.CID) != 32 || len(entry.StorageID) != 32 {
			continue
		}
		var cid, sid [32]byte
		copy(cid[:], entry.CID)
		copy(sid[:], entry.StorageID)
		if err := e.idx.PutCID(cid, sid); err != nil {
			return true, restored, fmt.Errorf("restore cid mapping: %w", err)
		}
		restored++
	}
	return true, restored, nil
}
