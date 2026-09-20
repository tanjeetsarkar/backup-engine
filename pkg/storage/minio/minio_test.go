package minio

import (
	"testing"

	miniosdk "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	client, err := miniosdk.New("localhost:9000", &miniosdk.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Secure: false,
	})
	if err != nil {
		t.Fatalf("new minio client: %v", err)
	}
	storage, err := New(client, "test-bucket", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return storage
}

func TestSetObjectLockRetentionConfiguresFields(t *testing.T) {
	storage := newTestStorage(t)
	if storage.objectLockRetentionDays != 0 {
		t.Fatalf("expected retention disabled by default, got %d", storage.objectLockRetentionDays)
	}

	storage.SetObjectLockRetention(30, false)
	if storage.objectLockRetentionDays != 30 || storage.objectLockCompliance {
		t.Fatalf("governance retention not applied: days=%d compliance=%v", storage.objectLockRetentionDays, storage.objectLockCompliance)
	}

	storage.SetObjectLockRetention(365, true)
	if storage.objectLockRetentionDays != 365 || !storage.objectLockCompliance {
		t.Fatalf("compliance retention not applied: days=%d compliance=%v", storage.objectLockRetentionDays, storage.objectLockCompliance)
	}
}

func TestEnsureObjectLockEnabledNoOpWithoutRetentionConfigured(t *testing.T) {
	storage := newTestStorage(t)
	if err := storage.EnsureObjectLockEnabled(t.Context()); err != nil {
		t.Fatalf("expected no-op when retention is disabled, got %v", err)
	}
}
