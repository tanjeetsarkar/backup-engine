package pipeline

import (
	"encoding/json"
	"fmt"
	"time"

	backupcrypto "github.com/tanjeetsarkar/backup-engine/pkg/crypto"
	"github.com/tanjeetsarkar/backup-engine/pkg/index"
)

const transactionLogVersion = 1

// TransactionLogEntry is one persisted, encrypted record of a completed (or failed) operation.
type TransactionLogEntry struct {
	Version     int             `json:"version"`
	Operation   Operation       `json:"operation"`
	Status      string          `json:"status"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt time.Time       `json:"completed_at"`
	Duration    time.Duration   `json:"duration"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// historyEnvelope is the encrypted-at-rest wrapper for a TransactionLogEntry, matching the
// nonce/ciphertext convention used elsewhere (e.g. manifest.SnapshotEnvelope, SnapshotLifecycle).
type historyEnvelope struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func buildTransactionLogEntry(operation Operation, startedAt time.Time, opErr error, result any) TransactionLogEntry {
	entry := TransactionLogEntry{
		Version:     transactionLogVersion,
		Operation:   operation,
		StartedAt:   startedAt.UTC(),
		CompletedAt: time.Now().UTC(),
		Duration:    time.Since(startedAt),
	}
	if resultJSON, marshalErr := json.Marshal(result); marshalErr == nil {
		entry.Result = resultJSON
	}
	if opErr != nil {
		entry.Status = "failed"
		entry.Error = opErr.Error()
	} else {
		entry.Status = "success"
	}
	return entry
}

// persistTransactionLog encrypts and appends one transaction history entry. Failures here are
// deliberately swallowed by callers (via recordTransaction): a history-logging problem must never
// fail the primary operation it describes.
func persistTransactionLog(idx *index.DB, km *backupcrypto.KeyManager, entry TransactionLogEntry) error {
	if idx == nil || km == nil {
		return nil
	}
	plain, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	ciphertext, nonce, err := km.EncryptMetadata(plain)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(historyEnvelope{Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return err
	}
	return idx.PutTransactionLog(entry.CompletedAt, raw)
}

// recordTransaction is a best-effort append to persistent transaction history; it never returns an
// error to the caller since it runs from defers around the primary operations.
func (e *Engine) recordTransaction(operation Operation, startedAt time.Time, opErr error, result any) {
	_ = persistTransactionLog(e.idx, e.km, buildTransactionLogEntry(operation, startedAt, opErr, result))
}

// ListTransactionHistory returns the most recent transaction history entries, newest first.
// limit <= 0 returns every entry.
func (e *Engine) ListTransactionHistory(limit int) ([]TransactionLogEntry, error) {
	rawEntries, err := e.idx.ListTransactionLogs(limit)
	if err != nil {
		return nil, err
	}
	out := make([]TransactionLogEntry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		var envelope historyEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("decode transaction history envelope: %w", err)
		}
		plain, err := e.km.DecryptMetadata(envelope.Ciphertext, envelope.Nonce)
		if err != nil {
			return nil, fmt.Errorf("decrypt transaction history entry: %w", err)
		}
		var entry TransactionLogEntry
		if err := json.Unmarshal(plain, &entry); err != nil {
			return nil, fmt.Errorf("decode transaction history entry: %w", err)
		}
		out = append(out, entry)
	}
	return out, nil
}

// ClearTransactionHistoryBefore deletes history entries completed before cutoff, returning the
// number of entries removed.
func (e *Engine) ClearTransactionHistoryBefore(cutoff time.Time) (int, error) {
	return e.idx.DeleteTransactionLogsBefore(cutoff)
}
