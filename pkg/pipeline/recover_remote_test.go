package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
	"github.com/tanjeetsarkar/backup-engine/pkg/replicate"
)

func TestRecoverFromEnginesRebuildsRepositoryFromRemote(t *testing.T) {
	source, snapshotID := createManagedSnapshot(t)
	defer source.Close()

	remotePacks, err := pack.NewLocalFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new remote pack storage: %v", err)
	}
	remoteManifests, err := pack.NewLocalFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new remote manifest storage: %v", err)
	}
	remoteIndex, err := pack.NewLocalFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new remote index storage: %v", err)
	}

	ctx := context.Background()
	if _, err := replicate.NewDaemon(source.storage, remotePacks).Run(ctx, nil); err != nil {
		t.Fatalf("replicate packs to fake remote: %v", err)
	}
	if _, err := replicate.NewDaemon(newManifestSourceAdapter(source.idx), remoteManifests).Run(ctx, nil); err != nil {
		t.Fatalf("replicate manifests to fake remote: %v", err)
	}
	if _, err := source.replicateCIDDirectory(ctx, remoteIndex); err != nil {
		t.Fatalf("replicate cid directory to fake remote: %v", err)
	}

	freshRepo := t.TempDir()
	fresh, err := Open(EngineConfig{RepoDir: freshRepo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatalf("open fresh repository: %v", err)
	}
	defer fresh.Close()

	result, err := fresh.recoverFromEngines(ctx, remoteManifests, remotePacks, remoteIndex, nil)
	if err != nil {
		t.Fatalf("recoverFromEngines: %v", err)
	}
	if result.ManifestsRecovered != 1 {
		t.Fatalf("expected 1 recovered manifest, got %d", result.ManifestsRecovered)
	}
	if result.PacksScanned == 0 {
		t.Fatalf("expected at least one pack scanned")
	}
	if result.ChunkLocations == 0 {
		t.Fatalf("expected at least one recovered chunk location")
	}
	if !result.CIDDirectoryFound || result.CIDsRecovered == 0 {
		t.Fatalf("expected cid directory to be recovered, got %+v", result)
	}
	if result.Verify.SnapshotsChecked != 1 {
		t.Fatalf("expected recovered repository to verify 1 snapshot, got %+v", result.Verify)
	}

	destDir := t.TempDir()
	if err := fresh.RestoreSnapshot(ctx, snapshotID, destDir); err != nil {
		t.Fatalf("restore from recovered repository: %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(destDir, "file.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if !bytes.Equal(restored, []byte("managed snapshot")) {
		t.Fatalf("restored content mismatch: got %q", restored)
	}
}

func TestRecoverFromEnginesWithoutCIDDirectoryStillListsButCannotRestore(t *testing.T) {
	source, _ := createManagedSnapshot(t)
	defer source.Close()

	remotePacks, err := pack.NewLocalFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new remote pack storage: %v", err)
	}
	remoteManifests, err := pack.NewLocalFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new remote manifest storage: %v", err)
	}
	remoteIndex, err := pack.NewLocalFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new remote index storage: %v", err)
	}

	ctx := context.Background()
	if _, err := replicate.NewDaemon(source.storage, remotePacks).Run(ctx, nil); err != nil {
		t.Fatalf("replicate packs to fake remote: %v", err)
	}
	if _, err := replicate.NewDaemon(newManifestSourceAdapter(source.idx), remoteManifests).Run(ctx, nil); err != nil {
		t.Fatalf("replicate manifests to fake remote: %v", err)
	}
	// Deliberately skip replicating the CID directory, simulating a repository replicated before
	// that feature existed.

	fresh, err := Open(EngineConfig{RepoDir: t.TempDir(), Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatalf("open fresh repository: %v", err)
	}
	defer fresh.Close()

	if _, err := fresh.recoverFromEngines(ctx, remoteManifests, remotePacks, remoteIndex, nil); err == nil {
		t.Fatal("expected recovery to fail verification without a replicated cid directory")
	}
}

func TestRecoverFromRemoteDetailedRejectsLocalRemoteBackend(t *testing.T) {
	if _, err := RecoverFromRemoteDetailed(context.Background(), RecoverConfig{Remote: StorageConfig{Backend: StorageBackendLocal}}, nil); err == nil {
		t.Fatal("expected error when recovery remote backend is local")
	}
	if _, err := RecoverFromRemoteDetailed(context.Background(), RecoverConfig{}, nil); err == nil {
		t.Fatal("expected error when recovery remote backend is unset (defaults to local)")
	}
}
