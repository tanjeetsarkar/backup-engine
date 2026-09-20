package pack

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/zeebo/blake3"
)

var (
	PackfileMagic              = [4]byte{0x50, 0x41, 0x43, 0x4B} // "PACK"
	CurrentVersion      uint16 = 1
	ErrInvalidPackfile         = errors.New("invalid packfile: signature mismatch or corrupt header")
	ErrInvalidNonceSize        = errors.New("invalid nonce size: pack records require a 24-byte nonce")
)

const packNonceSize = 24

// StorageEngine defines the transport and storage interface for local and cloud tiers.
type StorageEngine interface {
	PutPack(ctx context.Context, packID [32]byte, r io.Reader, size int64) error
	GetPack(ctx context.Context, packID [32]byte) (io.ReadCloser, error)
	GetChunkRange(ctx context.Context, packID [32]byte, offset int64, length int64) ([]byte, error)
	DeletePack(ctx context.Context, packID [32]byte) error
	ListPacks(ctx context.Context) ([][32]byte, error)
}

// ChunkEntry details an indexed chunk contained within a packfile.
type ChunkEntry struct {
	StorageID [32]byte
	Offset    uint64
	Length    uint32
}

// PackfileBuilder handles chunk aggregation into a single contiguous packfile.
type PackfileBuilder struct {
	buffer     *bytes.Buffer
	entries    []ChunkEntry
	targetSize int
}

// NewPackfileBuilder creates a builder targeting specified byte sizes (e.g. 16 MiB).
func NewPackfileBuilder(targetSize int) *PackfileBuilder {
	buf := new(bytes.Buffer)
	// Reserve 8-byte space for Header: Magic (4B), Version (2B), Count (2B)
	header := make([]byte, 8)
	copy(header[0:4], PackfileMagic[:])
	binary.BigEndian.PutUint16(header[4:6], CurrentVersion)
	binary.BigEndian.PutUint16(header[6:8], 0) // Placeholder for count
	buf.Write(header)

	return &PackfileBuilder{
		buffer:     buf,
		targetSize: targetSize,
	}
}

// Append adds an encrypted chunk and its metadata to the packfile.
func (p *PackfileBuilder) Append(storageID [32]byte, nonce []byte, ciphertext []byte) error {
	if len(nonce) != packNonceSize {
		return ErrInvalidNonceSize
	}

	offset := uint64(p.buffer.Len())

	// Write Chunk Record: [StorageID (32B)] [Nonce (24B)] [CipherLen (4B)] [Ciphertext]
	p.buffer.Write(storageID[:])
	p.buffer.Write(nonce)

	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(ciphertext)))
	p.buffer.Write(lenBuf)
	p.buffer.Write(ciphertext)

	p.entries = append(p.entries, ChunkEntry{
		StorageID: storageID,
		Offset:    offset,
		Length:    uint32(len(ciphertext)),
	})

	return nil
}

// IsFull checks if the accumulated buffer exceeds the configured threshold.
func (p *PackfileBuilder) IsFull() bool {
	return p.buffer.Len() >= p.targetSize
}

// Finalize commits the trailing index, updates the count, and generates the final pack buffer.
func (p *PackfileBuilder) Finalize() ([]byte, [32]byte, []ChunkEntry, error) {
	data := p.buffer.Bytes()

	// Update chunk count in header
	binary.BigEndian.PutUint16(data[6:8], uint16(len(p.entries)))

	tailOffset := uint64(len(data))

	// Write Tail Index
	for _, entry := range p.entries {
		p.buffer.Write(entry.StorageID[:])
		var entryMeta [12]byte
		binary.BigEndian.PutUint64(entryMeta[0:8], entry.Offset)
		binary.BigEndian.PutUint32(entryMeta[8:12], entry.Length)
		p.buffer.Write(entryMeta[:])
	}

	// Write Trailer: [TailOffset (8B)] [BLAKE3 Checksum (32B)]
	var trailer [40]byte
	binary.BigEndian.PutUint64(trailer[0:8], tailOffset)

	fullContent := p.buffer.Bytes()
	checksum := blake3.Sum256(fullContent)
	copy(trailer[8:40], checksum[:])

	p.buffer.Write(trailer[:])

	finalPackBytes := p.buffer.Bytes()
	packID := blake3.Sum256(finalPackBytes)

	return finalPackBytes, packID, p.entries, nil
}

// LocalFilesystemStorage implements the StorageEngine interface over local disk/NVMe.
type LocalFilesystemStorage struct {
	baseDir string
	mu      sync.RWMutex
}

func NewLocalFilesystemStorage(baseDir string) (*LocalFilesystemStorage, error) {
	if err := os.MkdirAll(filepath.Join(baseDir, "packs"), 0750); err != nil {
		return nil, err
	}
	return &LocalFilesystemStorage{baseDir: baseDir}, nil
}

func (l *LocalFilesystemStorage) packPath(packID [32]byte) string {
	name := fmt.Sprintf("%x.pack", packID)
	return filepath.Join(l.baseDir, "packs", name)
}

func (l *LocalFilesystemStorage) PutPack(ctx context.Context, packID [32]byte, r io.Reader, size int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	dstPath := l.packPath(packID)
	tmpPath := dstPath + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := f.Sync(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, dstPath)
}

func (l *LocalFilesystemStorage) GetPack(ctx context.Context, packID [32]byte) (io.ReadCloser, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return os.Open(l.packPath(packID))
}

func (l *LocalFilesystemStorage) GetChunkRange(ctx context.Context, packID [32]byte, offset int64, length int64) ([]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	f, err := os.Open(l.packPath(packID))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil, err
	}

	return buf, nil
}

func (l *LocalFilesystemStorage) DeletePack(ctx context.Context, packID [32]byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return os.Remove(l.packPath(packID))
}

func (l *LocalFilesystemStorage) ListPacks(ctx context.Context) ([][32]byte, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	pattern := filepath.Join(l.baseDir, "packs", "*.pack")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	var results [][32]byte
	for _, match := range matches {
		var id [32]byte
		base := filepath.Base(match)
		hexStr := base[:len(base)-len(".pack")]
		if _, err := fmt.Sscanf(hexStr, "%x", &id); err == nil {
			results = append(results, id)
		}
	}

	return results, nil
}
