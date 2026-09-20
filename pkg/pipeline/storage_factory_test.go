package pipeline

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestNewStorageEngineLocalDefault(t *testing.T) {
	repoDir := t.TempDir()
	storage, err := NewStorageEngine(repoDir, StorageConfig{})
	if err != nil {
		t.Fatalf("NewStorageEngine: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "data")); err != nil {
		t.Fatalf("expected local data directory to exist: %v", err)
	}
	if _, err := storage.ListPacks(context.Background()); err != nil {
		t.Fatalf("ListPacks on fresh local backend: %v", err)
	}
}

func TestNewStorageEngineMinIORequiresConnectionDetails(t *testing.T) {
	repoDir := t.TempDir()
	cases := []StorageConfig{
		{Backend: StorageBackendMinIO},
		{Backend: StorageBackendMinIO, Endpoint: "localhost:9000"},
		{Backend: StorageBackendMinIO, Endpoint: "localhost:9000", Bucket: "backups"},
	}
	for _, cfg := range cases {
		if _, err := NewStorageEngine(repoDir, cfg); err == nil {
			t.Fatalf("expected error for incomplete minio config %+v", cfg)
		}
	}
}

func TestNewStorageEngineUnknownBackend(t *testing.T) {
	repoDir := t.TempDir()
	if _, err := NewStorageEngine(repoDir, StorageConfig{Backend: "azure"}); err == nil {
		t.Fatal("expected error for unknown storage backend")
	}
}

func TestEnsureObjectLockEnabledNoOpForLocalOrDisabled(t *testing.T) {
	if err := EnsureObjectLockEnabled(context.Background(), StorageConfig{}); err != nil {
		t.Fatalf("expected no-op for local backend, got %v", err)
	}
	if err := EnsureObjectLockEnabled(context.Background(), StorageConfig{Backend: StorageBackendMinIO}); err != nil {
		t.Fatalf("expected no-op when ObjectLockRetentionDays is unset, got %v", err)
	}
}

// fakeRemoteStorage is a minimal in-memory pack.StorageEngine standing in for a remote backend
// (e.g. MinIO) so backup/restore can be exercised end-to-end without a running server.
type fakeRemoteStorage struct {
	packs map[[32]byte][]byte
}

func newFakeRemoteStorage() *fakeRemoteStorage {
	return &fakeRemoteStorage{packs: make(map[[32]byte][]byte)}
}

func (s *fakeRemoteStorage) PutPack(_ context.Context, packID [32]byte, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.packs[packID] = data
	return nil
}

func (s *fakeRemoteStorage) GetPack(_ context.Context, packID [32]byte) (io.ReadCloser, error) {
	data, ok := s.packs[packID]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeRemoteStorage) GetChunkRange(_ context.Context, packID [32]byte, offset int64, length int64) ([]byte, error) {
	data, ok := s.packs[packID]
	if !ok {
		return nil, os.ErrNotExist
	}
	out := make([]byte, length)
	copy(out, data[offset:offset+length])
	return out, nil
}

func (s *fakeRemoteStorage) DeletePack(_ context.Context, packID [32]byte) error {
	if _, ok := s.packs[packID]; !ok {
		return os.ErrNotExist
	}
	delete(s.packs, packID)
	return nil
}

func (s *fakeRemoteStorage) ListPacks(_ context.Context) ([][32]byte, error) {
	ids := make([][32]byte, 0, len(s.packs))
	for id := range s.packs {
		ids = append(ids, id)
	}
	return ids, nil
}

func TestOpenBackupRestoreAgainstInjectedRemoteStorage(t *testing.T) {
	repoDir := t.TempDir()
	sourceDir := t.TempDir()
	restoreDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "file.txt"), []byte("remote-backed content"), 0640); err != nil {
		t.Fatal(err)
	}

	remote := newFakeRemoteStorage()
	engine, err := Open(EngineConfig{RepoDir: repoDir, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef"), Storage: remote})
	if err != nil {
		t.Fatalf("Open with injected remote storage: %v", err)
	}
	defer engine.Close()

	if _, err := os.Stat(filepath.Join(repoDir, "data")); err == nil {
		t.Fatalf("expected no local data directory to be created when a remote storage engine is injected")
	}

	snapshotID, err := engine.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath: %v", err)
	}
	if len(remote.packs) == 0 {
		t.Fatal("expected backup to write at least one pack to the injected remote storage")
	}

	if err := engine.RestoreSnapshot(context.Background(), snapshotID, restoreDir); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(restoreDir, "file.txt"))
	if err != nil || string(content) != "remote-backed content" {
		t.Fatalf("unexpected restored content: data=%q err=%v", content, err)
	}
}
