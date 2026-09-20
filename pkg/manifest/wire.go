package manifest

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Manifest plaintext wire format. Field numbers below are load-bearing on-disk contracts;
// never renumber or reuse a retired number. Unknown fields are skipped on read for forward
// compatibility with future additions.
//
// FileNode: 1 path, 2 size, 3 mode, 4 mod_time_epoch, 5 storage_ids (repeated 32B),
//
//	6 content_hash (32B), 7 uid, 8 gid, 9 symlink_target, 10 xattrs (repeated {1 name, 2 value})
//
// DirectoryNode: 1 path, 2 subdirs (repeated), 3 files (repeated), 4 subtree_hash (32B)
// SnapshotManifest: 1 snapshot_id (32B), 2 parent_snapshot_id (32B, optional), 3 timestamp
//
//	(RFC3339Nano string), 4 retention_tags (repeated string), 5 root, 6 merkle_root (32B),
//	7 total_bytes, 8 total_files
const manifestWireFormat = "protobuf-v2"

func appendXAttr(b []byte, attr XAttr) []byte {
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte(attr.Name))
	inner = protowire.AppendTag(inner, 2, protowire.BytesType)
	inner = protowire.AppendBytes(inner, attr.Value)
	b = protowire.AppendTag(b, 10, protowire.BytesType)
	b = protowire.AppendBytes(b, inner)
	return b
}

func consumeXAttr(b []byte) (XAttr, error) {
	var attr XAttr
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return attr, protowire.ParseError(n)
		}
		b = b[n:]
		switch num {
		case 1:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return attr, protowire.ParseError(n)
			}
			attr.Name = string(v)
			b = b[n:]
		case 2:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return attr, protowire.ParseError(n)
			}
			attr.Value = append([]byte(nil), v...)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return attr, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return attr, nil
}

func appendFileNode(b []byte, fn *FileNode) []byte {
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte(fn.Path))
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(fn.Size))
	b = protowire.AppendTag(b, 3, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(fn.Mode))
	b = protowire.AppendTag(b, 4, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(fn.ModTimeEpoch))
	for _, sid := range fn.StorageIDs {
		b = protowire.AppendTag(b, 5, protowire.BytesType)
		b = protowire.AppendBytes(b, sid[:])
	}
	b = protowire.AppendTag(b, 6, protowire.BytesType)
	b = protowire.AppendBytes(b, fn.ContentHash[:])
	if fn.UID != 0 {
		b = protowire.AppendTag(b, 7, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(fn.UID))
	}
	if fn.GID != 0 {
		b = protowire.AppendTag(b, 8, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(fn.GID))
	}
	if fn.SymlinkTarget != "" {
		b = protowire.AppendTag(b, 9, protowire.BytesType)
		b = protowire.AppendBytes(b, []byte(fn.SymlinkTarget))
	}
	for _, attr := range fn.XAttrs {
		b = appendXAttr(b, attr)
	}
	return b
}

func consumeFileNode(b []byte) (*FileNode, error) {
	fn := &FileNode{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		switch num {
		case 1:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			fn.Path = string(v)
			b = b[n:]
		case 2:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			fn.Size = int64(v)
			b = b[n:]
		case 3:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			fn.Mode = uint32(v)
			b = b[n:]
		case 4:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			fn.ModTimeEpoch = int64(v)
			b = b[n:]
		case 5:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			if len(v) != 32 {
				return nil, fmt.Errorf("manifest wire: storage id must be 32 bytes, got %d", len(v))
			}
			var sid [32]byte
			copy(sid[:], v)
			fn.StorageIDs = append(fn.StorageIDs, sid)
			b = b[n:]
		case 6:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			if len(v) != 32 {
				return nil, fmt.Errorf("manifest wire: content hash must be 32 bytes, got %d", len(v))
			}
			copy(fn.ContentHash[:], v)
			b = b[n:]
		case 7:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			fn.UID = uint32(v)
			b = b[n:]
		case 8:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			fn.GID = uint32(v)
			b = b[n:]
		case 9:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			fn.SymlinkTarget = string(v)
			b = b[n:]
		case 10:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			attr, err := consumeXAttr(v)
			if err != nil {
				return nil, err
			}
			fn.XAttrs = append(fn.XAttrs, attr)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return fn, nil
}

func appendDirectoryNode(b []byte, dn *DirectoryNode) []byte {
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte(dn.Path))
	for _, sub := range dn.Subdirs {
		var inner []byte
		inner = appendDirectoryNode(inner, sub)
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, inner)
	}
	for _, file := range dn.Files {
		var inner []byte
		inner = appendFileNode(inner, file)
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, inner)
	}
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendBytes(b, dn.SubtreeHash[:])
	return b
}

func consumeDirectoryNode(b []byte) (*DirectoryNode, error) {
	dn := &DirectoryNode{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		switch num {
		case 1:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			dn.Path = string(v)
			b = b[n:]
		case 2:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			sub, err := consumeDirectoryNode(v)
			if err != nil {
				return nil, err
			}
			dn.Subdirs = append(dn.Subdirs, sub)
			b = b[n:]
		case 3:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			file, err := consumeFileNode(v)
			if err != nil {
				return nil, err
			}
			dn.Files = append(dn.Files, file)
			b = b[n:]
		case 4:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			if len(v) != 32 {
				return nil, fmt.Errorf("manifest wire: subtree hash must be 32 bytes, got %d", len(v))
			}
			copy(dn.SubtreeHash[:], v)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return dn, nil
}

// MarshalSnapshotWire encodes a snapshot manifest using the protobuf wire format.
func MarshalSnapshotWire(snapshot *SnapshotManifest) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot cannot be nil")
	}
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, snapshot.SnapshotID[:])
	if snapshot.ParentSnapshotID != nil {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, snapshot.ParentSnapshotID[:])
	}
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte(snapshot.Timestamp.UTC().Format(time.RFC3339Nano)))
	for _, tag := range snapshot.RetentionTags {
		b = protowire.AppendTag(b, 4, protowire.BytesType)
		b = protowire.AppendBytes(b, []byte(tag))
	}
	if snapshot.Root != nil {
		var inner []byte
		inner = appendDirectoryNode(inner, snapshot.Root)
		b = protowire.AppendTag(b, 5, protowire.BytesType)
		b = protowire.AppendBytes(b, inner)
	}
	b = protowire.AppendTag(b, 6, protowire.BytesType)
	b = protowire.AppendBytes(b, snapshot.MerkleRoot[:])
	b = protowire.AppendTag(b, 7, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(snapshot.TotalBytes))
	b = protowire.AppendTag(b, 8, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(snapshot.TotalFiles))
	return b, nil
}

// UnmarshalSnapshotWire decodes a protobuf-wire-encoded snapshot manifest.
func UnmarshalSnapshotWire(raw []byte) (*SnapshotManifest, error) {
	sm := &SnapshotManifest{}
	b := raw
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		switch num {
		case 1:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			if len(v) != 32 {
				return nil, fmt.Errorf("manifest wire: snapshot id must be 32 bytes, got %d", len(v))
			}
			copy(sm.SnapshotID[:], v)
			b = b[n:]
		case 2:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			if len(v) != 32 {
				return nil, fmt.Errorf("manifest wire: parent snapshot id must be 32 bytes, got %d", len(v))
			}
			var parent [32]byte
			copy(parent[:], v)
			sm.ParentSnapshotID = &parent
			b = b[n:]
		case 3:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			ts, err := time.Parse(time.RFC3339Nano, string(v))
			if err != nil {
				return nil, fmt.Errorf("manifest wire: parse timestamp: %w", err)
			}
			sm.Timestamp = ts.UTC()
			b = b[n:]
		case 4:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			sm.RetentionTags = append(sm.RetentionTags, string(v))
			b = b[n:]
		case 5:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			root, err := consumeDirectoryNode(v)
			if err != nil {
				return nil, err
			}
			sm.Root = root
			b = b[n:]
		case 6:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			if len(v) != 32 {
				return nil, fmt.Errorf("manifest wire: merkle root must be 32 bytes, got %d", len(v))
			}
			copy(sm.MerkleRoot[:], v)
			b = b[n:]
		case 7:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			sm.TotalBytes = int64(v)
			b = b[n:]
		case 8:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			sm.TotalFiles = int64(v)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return sm, nil
}
