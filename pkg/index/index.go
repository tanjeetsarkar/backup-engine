package index

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketCIDs               = []byte("cids")
	bucketCIDBySID           = []byte("cid_by_sid")
	bucketChunks             = []byte("chunks")
	bucketSnapshots          = []byte("snapshots")
	bucketSnapshotLifecycle  = []byte("snapshot_lifecycle")
	bucketMeta               = []byte("meta")
	bucketTransactionHistory = []byte("transaction_history")

	ErrCorruptChunkLocation = errors.New("corrupt chunk location record")
)

const chunkLocationSize = 32 + 8 + 4 + 8

// ChunkLocation stores where a chunk is physically located and when it was uploaded.
type ChunkLocation struct {
	PackID         [32]byte
	Offset         uint64
	Length         uint32
	UploadTimeUnix int64
}

// ChunkRecord couples a StorageID to its physical location.
type ChunkRecord struct {
	StorageID [32]byte
	Location  ChunkLocation
}

// ChunkMapping contains all index records committed for one stored chunk.
type ChunkMapping struct {
	CID       [32]byte
	StorageID [32]byte
	Location  ChunkLocation
}

// PutChunkMappings atomically writes CID, reverse-CID, and location records.
func (d *DB) PutChunkMappings(mappings []ChunkMapping) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		cidBucket := tx.Bucket(bucketCIDs)
		reverseBucket := tx.Bucket(bucketCIDBySID)
		chunkBucket := tx.Bucket(bucketChunks)
		if cidBucket == nil || reverseBucket == nil || chunkBucket == nil {
			return fmt.Errorf("missing chunk index bucket")
		}
		for _, mapping := range mappings {
			encoded := encodeChunkLocation(mapping.Location)
			if err := cidBucket.Put(mapping.CID[:], mapping.StorageID[:]); err != nil {
				return err
			}
			if err := reverseBucket.Put(mapping.StorageID[:], mapping.CID[:]); err != nil {
				return err
			}
			if err := chunkBucket.Put(mapping.StorageID[:], encoded[:]); err != nil {
				return err
			}
		}
		return nil
	})
}

// DB wraps the embedded bbolt index.
type DB struct {
	db *bolt.DB
}

// Open creates or opens an index database at dbPath and ensures required buckets exist.
func Open(dbPath string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
		return nil, fmt.Errorf("create index directory: %w", err)
	}

	db, err := bolt.Open(dbPath, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}

	idx := &DB{db: db}
	if err := idx.initBuckets(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return idx, nil
}

func (d *DB) initBuckets() error {
	return d.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketCIDs, bucketCIDBySID, bucketChunks, bucketSnapshots, bucketSnapshotLifecycle, bucketMeta, bucketTransactionHistory} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %q: %w", string(name), err)
			}
		}
		return nil
	})
}

// PutSnapshotLifecycle stores encrypted mutable lifecycle metadata for a snapshot.
func (d *DB) PutSnapshotLifecycle(snapshotID [32]byte, envelope []byte) error {
	payload := append([]byte(nil), envelope...)
	return d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketSnapshotLifecycle)
		if bucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketSnapshotLifecycle))
		}
		return bucket.Put(snapshotID[:], payload)
	})
}

// GetSnapshotLifecycle reads encrypted mutable lifecycle metadata for a snapshot.
func (d *DB) GetSnapshotLifecycle(snapshotID [32]byte) ([]byte, bool, error) {
	var envelope []byte
	found := false
	err := d.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketSnapshotLifecycle)
		if bucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketSnapshotLifecycle))
		}
		value := bucket.Get(snapshotID[:])
		if value == nil {
			return nil
		}
		envelope = append([]byte(nil), value...)
		found = true
		return nil
	})
	return envelope, found, err
}

// DeleteSnapshotLifecycle removes mutable lifecycle metadata for a snapshot.
func (d *DB) DeleteSnapshotLifecycle(snapshotID [32]byte) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketSnapshotLifecycle)
		if bucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketSnapshotLifecycle))
		}
		return bucket.Delete(snapshotID[:])
	})
}

// DeleteSnapshot removes a snapshot envelope and its lifecycle metadata atomically.
func (d *DB) DeleteSnapshot(snapshotID [32]byte) error {
	return d.DeleteSnapshots([][32]byte{snapshotID})
}

// DeleteSnapshots atomically removes snapshot envelopes and lifecycle metadata.
func (d *DB) DeleteSnapshots(snapshotIDs [][32]byte) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		snapshots := tx.Bucket(bucketSnapshots)
		lifecycle := tx.Bucket(bucketSnapshotLifecycle)
		if snapshots == nil || lifecycle == nil {
			return fmt.Errorf("missing snapshot bucket")
		}
		for _, snapshotID := range snapshotIDs {
			if err := snapshots.Delete(snapshotID[:]); err != nil {
				return err
			}
			if err := lifecycle.Delete(snapshotID[:]); err != nil {
				return err
			}
		}
		return nil
	})
}

// Close closes the underlying database.
func (d *DB) Close() error {
	return d.db.Close()
}

// PutCID stores a CID -> StorageID mapping.
func (d *DB) PutCID(cid, storageID [32]byte) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		cidBucket := tx.Bucket(bucketCIDs)
		revBucket := tx.Bucket(bucketCIDBySID)
		if cidBucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketCIDs))
		}
		if revBucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketCIDBySID))
		}
		if err := cidBucket.Put(cid[:], storageID[:]); err != nil {
			return err
		}
		return revBucket.Put(storageID[:], cid[:])
	})
}

// GetStorageID reads the StorageID for a CID.
func (d *DB) GetStorageID(cid [32]byte) (storageID [32]byte, found bool, err error) {
	err = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCIDs)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketCIDs))
		}
		value := b.Get(cid[:])
		if value == nil {
			found = false
			return nil
		}
		if len(value) != 32 {
			return fmt.Errorf("invalid storage id length: got %d", len(value))
		}
		copy(storageID[:], value)
		found = true
		return nil
	})
	return storageID, found, err
}

// GetCID returns the CID for a StorageID.
func (d *DB) GetCID(storageID [32]byte) (cid [32]byte, found bool, err error) {
	err = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketCIDBySID)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketCIDBySID))
		}
		value := b.Get(storageID[:])
		if value == nil {
			found = false
			return nil
		}
		if len(value) != 32 {
			return fmt.Errorf("invalid cid length: got %d", len(value))
		}
		copy(cid[:], value)
		found = true
		return nil
	})
	return cid, found, err
}

// PutChunkLocation stores StorageID -> ChunkLocation.
func (d *DB) PutChunkLocation(storageID [32]byte, loc ChunkLocation) error {
	encoded := encodeChunkLocation(loc)
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketChunks))
		}
		return b.Put(storageID[:], encoded[:])
	})
}

// UpsertChunkLocation stores StorageID -> ChunkLocation using discrete fields.
func (d *DB) UpsertChunkLocation(storageID [32]byte, packID [32]byte, offset uint64, length uint32, uploadTimeUnix int64) error {
	return d.PutChunkLocation(storageID, ChunkLocation{
		PackID:         packID,
		Offset:         offset,
		Length:         length,
		UploadTimeUnix: uploadTimeUnix,
	})
}

// GetChunkLocation reads the chunk location for a StorageID.
func (d *DB) GetChunkLocation(storageID [32]byte) (loc ChunkLocation, found bool, err error) {
	err = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketChunks))
		}
		value := b.Get(storageID[:])
		if value == nil {
			found = false
			return nil
		}
		decoded, decErr := decodeChunkLocation(value)
		if decErr != nil {
			return decErr
		}
		loc = decoded
		found = true
		return nil
	})
	return loc, found, err
}

// PutSnapshot stores an encrypted manifest envelope keyed by SnapshotID.
func (d *DB) PutSnapshot(snapshotID [32]byte, envelope []byte) error {
	payload := make([]byte, len(envelope))
	copy(payload, envelope)

	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketSnapshots))
		}
		return b.Put(snapshotID[:], payload)
	})
}

// CommitSnapshot stores a snapshot and removes its operation journal atomically.
func (d *DB) CommitSnapshot(snapshotID [32]byte, envelope []byte, journalKey string) error {
	payload := append([]byte(nil), envelope...)
	return d.db.Update(func(tx *bolt.Tx) error {
		snapshots := tx.Bucket(bucketSnapshots)
		meta := tx.Bucket(bucketMeta)
		if snapshots == nil || meta == nil {
			return fmt.Errorf("missing snapshot or metadata bucket")
		}
		if err := snapshots.Put(snapshotID[:], payload); err != nil {
			return err
		}
		return meta.Delete([]byte(journalKey))
	})
}

// GetSnapshot reads an encrypted manifest envelope by SnapshotID.
func (d *DB) GetSnapshot(snapshotID [32]byte) (envelope []byte, found bool, err error) {
	err = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketSnapshots))
		}
		value := b.Get(snapshotID[:])
		if value == nil {
			found = false
			return nil
		}

		envelope = make([]byte, len(value))
		copy(envelope, value)
		found = true
		return nil
	})
	return envelope, found, err
}

// ListSnapshotIDs returns all known snapshot IDs.
func (d *DB) ListSnapshotIDs() ([][32]byte, error) {
	var ids [][32]byte
	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketSnapshots))
		}

		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if len(k) != 32 {
				continue
			}
			var id [32]byte
			copy(id[:], k)
			ids = append(ids, id)
		}

		return nil
	})
	return ids, err
}

// ListChunkRecords returns all known chunk mappings in the index.
func (d *DB) ListChunkRecords() ([]ChunkRecord, error) {
	var records []ChunkRecord
	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketChunks))
		}

		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if len(k) != 32 {
				continue
			}

			loc, decErr := decodeChunkLocation(v)
			if decErr != nil {
				return decErr
			}

			var sid [32]byte
			copy(sid[:], k)
			records = append(records, ChunkRecord{StorageID: sid, Location: loc})
		}

		return nil
	})

	return records, err
}

// DeleteChunk removes chunk location and CID mappings for a StorageID.
func (d *DB) DeleteChunk(storageID [32]byte) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		cidBySID := tx.Bucket(bucketCIDBySID)
		cidBucket := tx.Bucket(bucketCIDs)
		chunkBucket := tx.Bucket(bucketChunks)

		if cidBySID == nil {
			return fmt.Errorf("missing bucket %q", string(bucketCIDBySID))
		}
		if cidBucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketCIDs))
		}
		if chunkBucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketChunks))
		}

		cid := cidBySID.Get(storageID[:])
		if cid != nil {
			if err := cidBucket.Delete(cid); err != nil {
				return err
			}
		}

		if err := cidBySID.Delete(storageID[:]); err != nil {
			return err
		}

		return chunkBucket.Delete(storageID[:])
	})
}

// PutMeta stores repository metadata by key.
func (d *DB) PutMeta(key string, value []byte) error {
	payload := make([]byte, len(value))
	copy(payload, value)

	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketMeta))
		}
		return b.Put([]byte(key), payload)
	})
}

// GetMeta reads repository metadata by key.
func (d *DB) GetMeta(key string) (value []byte, found bool, err error) {
	err = d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketMeta))
		}

		raw := b.Get([]byte(key))
		if raw == nil {
			found = false
			return nil
		}

		value = make([]byte, len(raw))
		copy(value, raw)
		found = true
		return nil
	})
	return value, found, err
}

// DeleteMeta removes repository metadata by key.
func (d *DB) DeleteMeta(key string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return fmt.Errorf("missing bucket %q", string(bucketMeta))
		}
		return b.Delete([]byte(key))
	})
}

// transactionHistoryKey orders entries chronologically: a big-endian UnixNano timestamp followed
// by a bucket-local sequence number that disambiguates same-nanosecond entries.
func transactionHistoryKey(seq uint64, completedAt time.Time) []byte {
	key := make([]byte, 16)
	binary.BigEndian.PutUint64(key[0:8], uint64(completedAt.UnixNano()))
	binary.BigEndian.PutUint64(key[8:16], seq)
	return key
}

// PutTransactionLog appends one encrypted transaction history entry.
func (d *DB) PutTransactionLog(completedAt time.Time, envelope []byte) error {
	payload := append([]byte(nil), envelope...)
	return d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketTransactionHistory)
		if bucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketTransactionHistory))
		}
		seq, err := bucket.NextSequence()
		if err != nil {
			return err
		}
		return bucket.Put(transactionHistoryKey(seq, completedAt), payload)
	})
}

// ListTransactionLogs returns encrypted transaction history entries, newest first. limit <= 0
// returns every entry.
func (d *DB) ListTransactionLogs(limit int) ([][]byte, error) {
	var entries [][]byte
	err := d.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketTransactionHistory)
		if bucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketTransactionHistory))
		}
		cursor := bucket.Cursor()
		for k, v := cursor.Last(); k != nil; k, v = cursor.Prev() {
			entries = append(entries, append([]byte(nil), v...))
			if limit > 0 && len(entries) >= limit {
				break
			}
		}
		return nil
	})
	return entries, err
}

// DeleteTransactionLogsBefore removes history entries completed strictly before cutoff, returning
// the number of entries removed.
func (d *DB) DeleteTransactionLogsBefore(cutoff time.Time) (int, error) {
	removed := 0
	err := d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketTransactionHistory)
		if bucket == nil {
			return fmt.Errorf("missing bucket %q", string(bucketTransactionHistory))
		}
		cutoffPrefix := make([]byte, 8)
		binary.BigEndian.PutUint64(cutoffPrefix, uint64(cutoff.UnixNano()))

		cursor := bucket.Cursor()
		var staleKeys [][]byte
		for k, _ := cursor.First(); k != nil; k, _ = cursor.Next() {
			if bytes.Compare(k[:8], cutoffPrefix) >= 0 {
				break
			}
			staleKeys = append(staleKeys, append([]byte(nil), k...))
		}
		for _, k := range staleKeys {
			if err := bucket.Delete(k); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}

func encodeChunkLocation(loc ChunkLocation) [chunkLocationSize]byte {
	var out [chunkLocationSize]byte
	copy(out[0:32], loc.PackID[:])
	binary.BigEndian.PutUint64(out[32:40], loc.Offset)
	binary.BigEndian.PutUint32(out[40:44], loc.Length)
	binary.BigEndian.PutUint64(out[44:52], uint64(loc.UploadTimeUnix))
	return out
}

func decodeChunkLocation(raw []byte) (ChunkLocation, error) {
	if len(raw) != chunkLocationSize {
		return ChunkLocation{}, ErrCorruptChunkLocation
	}

	var loc ChunkLocation
	copy(loc.PackID[:], raw[0:32])
	loc.Offset = binary.BigEndian.Uint64(raw[32:40])
	loc.Length = binary.BigEndian.Uint32(raw[40:44])
	loc.UploadTimeUnix = int64(binary.BigEndian.Uint64(raw[44:52]))
	return loc, nil
}
