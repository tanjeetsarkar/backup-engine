package pipeline

import (
	"fmt"
	"path/filepath"

	miniosdk "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
	miniostorage "github.com/tanjeetsarkar/backup-engine/pkg/storage/minio"
)

// StorageBackend selects which pack.StorageEngine implementation a repository uses.
type StorageBackend string

const (
	StorageBackendLocal StorageBackend = "local"
	StorageBackendMinIO StorageBackend = "minio"
)

// StorageConfig describes how to construct a pack.StorageEngine for a repository. The zero value
// (or Backend == StorageBackendLocal) selects the local filesystem backend under RepoDir/data.
type StorageConfig struct {
	Backend   StorageBackend
	Endpoint  string
	Bucket    string
	Prefix    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// NewStorageEngine builds the pack.StorageEngine described by cfg. repoDir is only used by the
// local backend; remote backends store everything in the configured bucket.
func NewStorageEngine(repoDir string, cfg StorageConfig) (pack.StorageEngine, error) {
	switch cfg.Backend {
	case "", StorageBackendLocal:
		return newLocalStorageEngine(repoDir)
	case StorageBackendMinIO, "s3":
		return newMinIOStorageEngine(cfg)
	default:
		return nil, fmt.Errorf("unknown storage backend %q (expected %q or %q)", cfg.Backend, StorageBackendLocal, StorageBackendMinIO)
	}
}

func newLocalStorageEngine(repoDir string) (pack.StorageEngine, error) {
	return pack.NewLocalFilesystemStorage(filepath.Join(repoDir, "data"))
}

func newMinIOStorageEngine(cfg StorageConfig) (pack.StorageEngine, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("minio storage backend requires an endpoint")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("minio storage backend requires a bucket")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("minio storage backend requires an access key and secret key")
	}

	client, err := miniosdk.New(cfg.Endpoint, &miniosdk.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	return miniostorage.New(client, cfg.Bucket, cfg.Prefix)
}
