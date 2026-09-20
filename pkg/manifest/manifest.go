package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zeebo/blake3"
)

// FileNode represents a filesystem leaf in the Merkle hierarchy.
type FileNode struct {
	Path         string     `json:"path"`
	Size         int64      `json:"size"`
	Mode         uint32     `json:"mode"`
	ModTimeEpoch int64      `json:"mod_time_epoch"`
	StorageIDs   [][32]byte `json:"storage_ids"`
	ContentHash  [32]byte   `json:"content_hash"`
}

// ComputeHash resolves the cryptographic integrity hash of a FileNode.
func (fn *FileNode) ComputeHash() [32]byte {
	h := blake3.New()
	h.Write([]byte(fn.Path))
	for _, sid := range fn.StorageIDs {
		h.Write(sid[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// DirectoryNode aggregates child files and subdirectories.
type DirectoryNode struct {
	Path        string           `json:"path"`
	Subdirs     []*DirectoryNode `json:"subdirs,omitempty"`
	Files       []*FileNode      `json:"files,omitempty"`
	SubtreeHash [32]byte         `json:"subtree_hash"`
}

// ComputeSubtreeHash recursively computes the Merkle root of a directory tree.
func (dn *DirectoryNode) ComputeSubtreeHash() [32]byte {
	h := blake3.New()
	h.Write([]byte(dn.Path))

	for _, d := range dn.Subdirs {
		childHash := d.ComputeSubtreeHash()
		h.Write(childHash[:])
	}

	for _, f := range dn.Files {
		fh := f.ComputeHash()
		h.Write(fh[:])
	}

	var out [32]byte
	copy(out[:], h.Sum(nil))
	dn.SubtreeHash = out
	return out
}

// SnapshotManifest describes an immutable point-in-time state.
type SnapshotManifest struct {
	SnapshotID       [32]byte       `json:"snapshot_id"`
	ParentSnapshotID *[32]byte      `json:"parent_snapshot_id,omitempty"`
	Timestamp        time.Time      `json:"timestamp"`
	RetentionTags    []string       `json:"retention_tags"`
	Root             *DirectoryNode `json:"root"`
	MerkleRoot       [32]byte       `json:"merkle_root"`
	TotalBytes       int64          `json:"total_bytes"`
	TotalFiles       int64          `json:"total_files"`
}

// NewSnapshotManifest builds and seals a point-in-time manifest.
func NewSnapshotManifest(parent *[32]byte, root *DirectoryNode, tags []string) (*SnapshotManifest, error) {
	if root == nil {
		return nil, errors.New("cannot create snapshot with nil directory root")
	}

	merkleRoot := root.ComputeSubtreeHash()

	sm := &SnapshotManifest{
		ParentSnapshotID: parent,
		Timestamp:        time.Now().UTC(),
		RetentionTags:    tags,
		Root:             root,
		MerkleRoot:       merkleRoot,
	}

	// Calculate totals
	sm.calculateStats(root)

	// Compute unique SnapshotID
	manifestData, err := json.Marshal(sm)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize manifest: %w", err)
	}

	sm.SnapshotID = blake3.Sum256(manifestData)
	return sm, nil
}

func (sm *SnapshotManifest) calculateStats(dn *DirectoryNode) {
	for _, f := range dn.Files {
		sm.TotalFiles++
		sm.TotalBytes += f.Size
	}
	for _, d := range dn.Subdirs {
		sm.calculateStats(d)
	}
}

// CollectAllStorageIDs traverses the Merkle tree and compiles all referenced chunks.
func (sm *SnapshotManifest) CollectAllStorageIDs() map[[32]byte]struct{} {
	refs := make(map[[32]byte]struct{})
	sm.walkCollect(sm.Root, refs)
	return refs
}

func (sm *SnapshotManifest) walkCollect(dn *DirectoryNode, refs map[[32]byte]struct{}) {
	if dn == nil {
		return
	}
	for _, f := range dn.Files {
		for _, sid := range f.StorageIDs {
			refs[sid] = struct{}{}
		}
	}
	for _, d := range dn.Subdirs {
		sm.walkCollect(d, refs)
	}
}
