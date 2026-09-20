package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	backupcrypto "github.com/tanjeetsarkar/backup-engine/pkg/crypto"
	"github.com/tanjeetsarkar/backup-engine/pkg/index"
	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
)

const repoKeyCheckMetaKey = "repo.keycheck.v1"

var (
	repoKeyCheckPayload = []byte("backup-engine-repository-key-check-v1")

	ErrRepositoryKeyMismatch = errors.New("repository key-check failed: passphrase/salt mismatch")
	ErrRepositoryNeedsInit   = errors.New("repository contains data but no key-check metadata; run init with bind-existing")
)

// InitConfig configures repository initialization.
type InitConfig struct {
	RepoDir      string
	Passphrase   []byte
	Salt         []byte
	BindExisting bool
}

// InitRepository initializes repository structure and key-check metadata.
func InitRepository(cfg InitConfig) error {
	_, err := InitRepositoryDetailed(cfg, nil)
	return err
}

// InitRepositoryDetailed initializes repository metadata and reports its phases.
func InitRepositoryDetailed(cfg InitConfig, reporter Reporter) (result InitResult, err error) {
	started := time.Now()
	result.Repository = cfg.RepoDir
	result.BoundExisting = cfg.BindExisting
	defer func() { result.Duration = time.Since(started) }()
	emit(reporter, ProgressEvent{Operation: OperationInit, Phase: PhasePreparing, Level: EventInfo, Message: "Preparing repository directories"})

	if cfg.RepoDir == "" {
		return result, fmt.Errorf("repo directory is required")
	}
	if len(cfg.Passphrase) == 0 {
		return result, fmt.Errorf("passphrase is required")
	}
	if len(cfg.Salt) < 16 {
		return result, fmt.Errorf("salt must be at least 16 bytes")
	}

	idx, err := index.Open(filepath.Join(cfg.RepoDir, "index", "index.db"))
	if err != nil {
		return result, err
	}
	defer idx.Close()

	if _, err := pack.NewLocalFilesystemStorage(filepath.Join(cfg.RepoDir, "data")); err != nil {
		return result, err
	}

	km, err := backupcrypto.NewKeyManager(cfg.Passphrase, cfg.Salt)
	if err != nil {
		return result, err
	}

	emit(reporter, ProgressEvent{Operation: OperationInit, Phase: PhaseCommitting, Level: EventInfo, Message: "Writing repository key-check metadata"})
	if err := ensureRepositoryKeyCheck(idx, km, cfg.BindExisting); err != nil {
		return result, err
	}
	result.KeyCheckReady = true
	emit(reporter, ProgressEvent{Operation: OperationInit, Phase: PhaseComplete, Level: EventSuccess, Message: "Repository initialization completed"})
	return result, nil
}

func ensureRepositoryKeyCheck(idx *index.DB, km *backupcrypto.KeyManager, bindExisting bool) error {
	raw, found, err := idx.GetMeta(repoKeyCheckMetaKey)
	if err != nil {
		return err
	}

	if found {
		var env manifest.SnapshotEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("invalid repository key-check metadata: %w", err)
		}

		plain, err := km.DecryptMetadata(env.Ciphertext, env.Nonce)
		if err != nil {
			if errors.Is(err, backupcrypto.ErrAuthenticationFailed) {
				return ErrRepositoryKeyMismatch
			}
			return err
		}
		if !bytes.Equal(plain, repoKeyCheckPayload) {
			return ErrRepositoryKeyMismatch
		}
		return nil
	}

	snapshots, err := idx.ListSnapshotIDs()
	if err != nil {
		return err
	}
	records, err := idx.ListChunkRecords()
	if err != nil {
		return err
	}

	if (len(snapshots) > 0 || len(records) > 0) && !bindExisting {
		return ErrRepositoryNeedsInit
	}

	ciphertext, nonce, err := km.EncryptMetadata(repoKeyCheckPayload)
	if err != nil {
		return err
	}

	env := manifest.SnapshotEnvelope{Nonce: nonce, Ciphertext: ciphertext}
	encoded, err := json.Marshal(env)
	if err != nil {
		return err
	}

	return idx.PutMeta(repoKeyCheckMetaKey, encoded)
}
