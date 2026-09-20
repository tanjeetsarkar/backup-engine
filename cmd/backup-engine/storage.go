package main

import (
	"flag"
	"os"

	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

// storageOptions binds CLI flags for selecting and configuring a repository's pack storage backend.
type storageOptions struct {
	backend   *string
	endpoint  *string
	bucket    *string
	prefix    *string
	accessKey *string
	secretKey *string
	useSSL    *bool
}

func bindStorageOptions(flags *flag.FlagSet) *storageOptions {
	return bindPrefixedStorageOptions(flags, "")
}

// bindRemoteStorageOptions binds the same set of flags under a "remote-" prefix, so a command like
// "replicate run" can configure its own repository's backend and a separate replication target
// without flag name collisions.
func bindRemoteStorageOptions(flags *flag.FlagSet) *storageOptions {
	return bindPrefixedStorageOptions(flags, "remote-")
}

func bindPrefixedStorageOptions(flags *flag.FlagSet, prefix string) *storageOptions {
	return &storageOptions{
		backend:   flags.String(prefix+"storage-backend", "local", "pack storage backend: local or minio (s3-compatible)"),
		endpoint:  flags.String(prefix+"s3-endpoint", "", "minio/s3 endpoint host:port (required for -storage-backend minio)"),
		bucket:    flags.String(prefix+"s3-bucket", "", "minio/s3 bucket name (required for -storage-backend minio)"),
		prefix:    flags.String(prefix+"s3-prefix", "", "optional object key prefix within the bucket"),
		accessKey: flags.String(prefix+"s3-access-key", "", "minio/s3 access key (falls back to AWS_ACCESS_KEY_ID)"),
		secretKey: flags.String(prefix+"s3-secret-key", "", "minio/s3 secret key (falls back to AWS_SECRET_ACCESS_KEY)"),
		useSSL:    flags.Bool(prefix+"s3-use-ssl", true, "use TLS when connecting to the minio/s3 endpoint"),
	}
}

func (o *storageOptions) config() pipeline.StorageConfig {
	accessKey := *o.accessKey
	if accessKey == "" {
		accessKey = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	secretKey := *o.secretKey
	if secretKey == "" {
		secretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	return pipeline.StorageConfig{
		Backend:   pipeline.StorageBackend(*o.backend),
		Endpoint:  *o.endpoint,
		Bucket:    *o.bucket,
		Prefix:    *o.prefix,
		AccessKey: accessKey,
		SecretKey: secretKey,
		UseSSL:    *o.useSSL,
	}
}

func (o *storageOptions) resolve(repoDir string) (pack.StorageEngine, error) {
	return pipeline.NewStorageEngine(repoDir, o.config())
}
