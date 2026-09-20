package minio

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	miniosdk "github.com/minio/minio-go/v7"
)

// Storage provides a MinIO/S3-compatible implementation of the pack.StorageEngine contract.
type Storage struct {
	client *miniosdk.Client
	bucket string
	prefix string

	// objectLockRetentionDays, when > 0, makes PutPack request S3 Object Lock retention on every
	// pack it uploads. The bucket must have been created with Object Lock enabled; it cannot be
	// retrofitted onto an existing bucket.
	objectLockRetentionDays int
	// objectLockCompliance selects COMPLIANCE mode (irreversible, even by the bucket owner/root
	// credentials) instead of the default GOVERNANCE mode (which a privileged, explicit
	// GovernanceBypass call can still override).
	objectLockCompliance bool
}

// New creates a MinIO storage backend.
func New(client *miniosdk.Client, bucket, prefix string) (*Storage, error) {
	if client == nil {
		return nil, fmt.Errorf("minio client cannot be nil")
	}
	if bucket == "" {
		return nil, fmt.Errorf("bucket is required")
	}

	return &Storage{
		client: client,
		bucket: bucket,
		prefix: strings.Trim(prefix, "/"),
	}, nil
}

// SetObjectLockRetention configures the S3 Object Lock retention every subsequently uploaded pack
// requests. retentionDays <= 0 disables it (the default).
func (s *Storage) SetObjectLockRetention(retentionDays int, compliance bool) {
	s.objectLockRetentionDays = retentionDays
	s.objectLockCompliance = compliance
}

// EnsureObjectLockEnabled fails clearly if Object Lock retention is configured but the bucket
// itself was not created with Object Lock enabled -- this is an S3-inherent limitation that cannot
// be retrofitted onto an existing bucket, so it must be surfaced explicitly rather than silently
// having every future PutPack call fail or (worse) silently not apply retention.
func (s *Storage) EnsureObjectLockEnabled(ctx context.Context) error {
	if s.objectLockRetentionDays <= 0 {
		return nil
	}
	enabled, _, _, _, err := s.client.GetObjectLockConfig(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("bucket %q does not have Object Lock enabled (it must be created with Object Lock support; this cannot be enabled retroactively): %w", s.bucket, err)
	}
	if !strings.EqualFold(enabled, "Enabled") {
		return fmt.Errorf("bucket %q does not have Object Lock enabled (it must be created with Object Lock support; this cannot be enabled retroactively)", s.bucket)
	}
	return nil
}

func (s *Storage) objectKey(packID [32]byte) string {
	name := fmt.Sprintf("%x.pack", packID)
	if s.prefix == "" {
		return name
	}
	return path.Join(s.prefix, name)
}

func (s *Storage) PutPack(ctx context.Context, packID [32]byte, r io.Reader, size int64) error {
	opts := miniosdk.PutObjectOptions{}
	if s.objectLockRetentionDays > 0 {
		opts.Mode = miniosdk.Governance
		if s.objectLockCompliance {
			opts.Mode = miniosdk.Compliance
		}
		opts.RetainUntilDate = time.Now().UTC().AddDate(0, 0, s.objectLockRetentionDays)
	}
	_, err := s.client.PutObject(ctx, s.bucket, s.objectKey(packID), r, size, opts)
	return err
}

func (s *Storage) GetPack(ctx context.Context, packID [32]byte) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s.objectKey(packID), miniosdk.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *Storage) GetChunkRange(ctx context.Context, packID [32]byte, offset int64, length int64) ([]byte, error) {
	opts := miniosdk.GetObjectOptions{}
	if err := opts.SetRange(offset, offset+length-1); err != nil {
		return nil, err
	}

	obj, err := s.client.GetObject(ctx, s.bucket, s.objectKey(packID), opts)
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	buf := make([]byte, length)
	if _, err := io.ReadFull(obj, buf); err != nil {
		return nil, err
	}

	return buf, nil
}

func (s *Storage) DeletePack(ctx context.Context, packID [32]byte) error {
	return s.client.RemoveObject(ctx, s.bucket, s.objectKey(packID), miniosdk.RemoveObjectOptions{})
}

func (s *Storage) ListPacks(ctx context.Context) ([][32]byte, error) {
	prefix := s.prefix
	if prefix != "" {
		prefix += "/"
	}

	ch := s.client.ListObjects(ctx, s.bucket, miniosdk.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})

	out := make([][32]byte, 0)
	for obj := range ch {
		if obj.Err != nil {
			return nil, obj.Err
		}

		base := path.Base(obj.Key)
		if !strings.HasSuffix(base, ".pack") {
			continue
		}

		hexPart := strings.TrimSuffix(base, ".pack")
		raw, err := hex.DecodeString(hexPart)
		if err != nil || len(raw) != 32 {
			continue
		}

		var id [32]byte
		copy(id[:], raw)
		out = append(out, id)
	}

	return out, nil
}
