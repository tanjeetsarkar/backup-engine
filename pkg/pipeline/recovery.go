package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/tanjeetsarkar/backup-engine/pkg/index"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
)

const backupJournalMetaKey = "operation.backup.v1"

type backupJournal struct {
	Version    int        `json:"version"`
	PackIDs    [][32]byte `json:"pack_ids"`
	StorageIDs [][32]byte `json:"storage_ids"`
}

func persistBackupJournal(database *index.DB, transaction *backupTransaction) error {
	journal := backupJournal{Version: 1, PackIDs: transaction.newPacks, StorageIDs: transaction.newStorageIDs}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return database.PutMeta(backupJournalMetaKey, raw)
}

func recoverInterruptedBackup(repoDir string, database *index.DB, storage pack.StorageEngine) error {
	raw, found, err := database.GetMeta(backupJournalMetaKey)
	if err != nil || !found {
		return err
	}
	var journal backupJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return fmt.Errorf("decode backup recovery journal: %w", err)
	}
	if journal.Version != 1 {
		return fmt.Errorf("unsupported backup recovery journal version %d", journal.Version)
	}
	lock, err := acquireMutationLock(repoDir)
	if err != nil {
		return err
	}
	defer lock.release()

	var recoveryErr error
	for _, storageID := range journal.StorageIDs {
		recoveryErr = errors.Join(recoveryErr, database.DeleteChunk(storageID))
	}
	for _, packID := range journal.PackIDs {
		if err := storage.DeletePack(context.Background(), packID); err != nil && !errors.Is(err, os.ErrNotExist) {
			recoveryErr = errors.Join(recoveryErr, err)
		}
	}
	if recoveryErr != nil {
		return recoveryErr
	}
	return database.DeleteMeta(backupJournalMetaKey)
}
