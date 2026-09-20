package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	backupcrypto "github.com/tanjeetsarkar/backup-engine/pkg/crypto"
	"github.com/tanjeetsarkar/backup-engine/pkg/manifest"
)

const (
	SnapshotLifecycleVersion = 1
	DefaultTrashRetention    = 7 * 24 * time.Hour
	maxSnapshotLabels        = 16
	maxSnapshotLabelLength   = 64
	maxSnapshotNoteLength    = 2048
)

// SnapshotState describes whether a snapshot is active or recoverably trashed.
type SnapshotState string

const (
	SnapshotActive  SnapshotState = "active"
	SnapshotTrashed SnapshotState = "trashed"
)

// SnapshotLifecycle stores mutable, encrypted snapshot-management metadata.
type SnapshotLifecycle struct {
	Version     int           `json:"version"`
	State       SnapshotState `json:"state"`
	Pinned      bool          `json:"pinned,omitempty"`
	RetainUntil *time.Time    `json:"retain_until,omitempty"`
	Labels      []string      `json:"labels,omitempty"`
	Note        string        `json:"note,omitempty"`
	TrashedAt   *time.Time    `json:"trashed_at,omitempty"`
	PurgeAfter  *time.Time    `json:"purge_after,omitempty"`
}

// SnapshotDetails combines immutable manifest facts with mutable lifecycle data.
type SnapshotDetails struct {
	ID               [32]byte
	Readable         bool
	StatusText       string
	Timestamp        time.Time
	TotalFiles       int64
	TotalBytes       int64
	RetentionTags    []string
	ParentSnapshotID *[32]byte
	Lifecycle        SnapshotLifecycle
}

// SnapshotMetadataUpdate changes user-managed labels and notes.
type SnapshotMetadataUpdate struct {
	Labels []string
	Note   string
}

func defaultSnapshotLifecycle() SnapshotLifecycle {
	return SnapshotLifecycle{Version: SnapshotLifecycleVersion, State: SnapshotActive}
}

// ListSnapshotDetails returns newest readable snapshots first and optionally includes trash.
func (e *Engine) ListSnapshotDetails(includeTrashed bool) ([]SnapshotDetails, error) {
	ids, err := e.ListSnapshots()
	if err != nil {
		return nil, err
	}
	details := make([]SnapshotDetails, 0, len(ids))
	for _, id := range ids {
		detail, err := e.GetSnapshotDetails(id)
		if err != nil {
			return nil, err
		}
		if !includeTrashed && detail.Lifecycle.State == SnapshotTrashed {
			continue
		}
		details = append(details, detail)
	}
	sort.Slice(details, func(i, j int) bool {
		left, right := details[i], details[j]
		if left.Timestamp.IsZero() != right.Timestamp.IsZero() {
			return !left.Timestamp.IsZero()
		}
		if !left.Timestamp.Equal(right.Timestamp) {
			return left.Timestamp.After(right.Timestamp)
		}
		return strings.Compare(fmt.Sprintf("%x", left.ID), fmt.Sprintf("%x", right.ID)) < 0
	})
	return details, nil
}

// GetSnapshotDetails loads immutable and mutable metadata for one snapshot.
func (e *Engine) GetSnapshotDetails(snapshotID [32]byte) (SnapshotDetails, error) {
	detail := SnapshotDetails{ID: snapshotID, Lifecycle: defaultSnapshotLifecycle()}
	envelope, found, err := e.idx.GetSnapshot(snapshotID)
	if err != nil {
		return detail, err
	}
	if !found {
		return detail, fmt.Errorf("snapshot not found")
	}
	snapshot, err := manifest.DecryptSnapshotEnvelope(envelope, e.km)
	if err != nil {
		if errors.Is(err, backupcrypto.ErrAuthenticationFailed) {
			detail.StatusText = "auth-failed"
		} else {
			detail.StatusText = "decode-error"
		}
		return detail, nil
	}
	detail.Readable = true
	detail.StatusText = "ok"
	detail.Timestamp = snapshot.Timestamp
	detail.TotalFiles = snapshot.TotalFiles
	detail.TotalBytes = snapshot.TotalBytes
	detail.RetentionTags = append([]string(nil), snapshot.RetentionTags...)
	detail.ParentSnapshotID = snapshot.ParentSnapshotID
	lifecycle, err := e.readSnapshotLifecycle(snapshotID)
	if err != nil {
		return detail, err
	}
	detail.Lifecycle = lifecycle
	return detail, nil
}

// TrashSnapshot moves a snapshot into recoverable trash.
func (e *Engine) TrashSnapshot(snapshotID [32]byte, retention time.Duration) (SnapshotLifecycle, error) {
	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	defer lock.release()
	if retention < 0 {
		return SnapshotLifecycle{}, fmt.Errorf("trash retention cannot be negative")
	}
	if retention == 0 {
		retention = DefaultTrashRetention
	}
	lifecycle, err := e.ensureManageableSnapshot(snapshotID)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	if lifecycle.State == SnapshotTrashed {
		return lifecycle, nil
	}
	now := time.Now().UTC()
	purgeAfter := now.Add(retention)
	lifecycle.State = SnapshotTrashed
	lifecycle.TrashedAt = &now
	lifecycle.PurgeAfter = &purgeAfter
	return lifecycle, e.writeSnapshotLifecycle(snapshotID, lifecycle)
}

// UntrashSnapshot restores a snapshot to active lifecycle state.
func (e *Engine) UntrashSnapshot(snapshotID [32]byte) (SnapshotLifecycle, error) {
	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	defer lock.release()
	lifecycle, err := e.ensureManageableSnapshot(snapshotID)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	lifecycle.State = SnapshotActive
	lifecycle.TrashedAt = nil
	lifecycle.PurgeAfter = nil
	return lifecycle, e.writeSnapshotLifecycle(snapshotID, lifecycle)
}

// SetSnapshotPin protects or unprotects an active snapshot from GFS expiration.
func (e *Engine) SetSnapshotPin(snapshotID [32]byte, pinned bool) (SnapshotLifecycle, error) {
	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	defer lock.release()
	lifecycle, err := e.ensureManageableSnapshot(snapshotID)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	lifecycle.Pinned = pinned
	return lifecycle, e.writeSnapshotLifecycle(snapshotID, lifecycle)
}

// SetSnapshotRetainUntil protects an active snapshot until a UTC deadline.
func (e *Engine) SetSnapshotRetainUntil(snapshotID [32]byte, retainUntil *time.Time) (SnapshotLifecycle, error) {
	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	defer lock.release()
	lifecycle, err := e.ensureManageableSnapshot(snapshotID)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	if retainUntil != nil {
		utc := retainUntil.UTC()
		lifecycle.RetainUntil = &utc
	} else {
		lifecycle.RetainUntil = nil
	}
	return lifecycle, e.writeSnapshotLifecycle(snapshotID, lifecycle)
}

// UpdateSnapshotMetadata changes encrypted user labels and note text.
func (e *Engine) UpdateSnapshotMetadata(snapshotID [32]byte, update SnapshotMetadataUpdate) (SnapshotLifecycle, error) {
	lock, err := acquireMutationLock(e.repoDir)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	defer lock.release()
	lifecycle, err := e.ensureManageableSnapshot(snapshotID)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	labels, err := validateSnapshotMetadata(update)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	lifecycle.Labels = labels
	lifecycle.Note = strings.TrimSpace(update.Note)
	return lifecycle, e.writeSnapshotLifecycle(snapshotID, lifecycle)
}

func validateSnapshotMetadata(update SnapshotMetadataUpdate) ([]string, error) {
	if len(update.Labels) > maxSnapshotLabels {
		return nil, fmt.Errorf("snapshot labels exceed maximum of %d", maxSnapshotLabels)
	}
	if len(update.Note) > maxSnapshotNoteLength {
		return nil, fmt.Errorf("snapshot note exceeds maximum of %d characters", maxSnapshotNoteLength)
	}
	labels := make([]string, 0, len(update.Labels))
	seen := make(map[string]struct{})
	for _, raw := range update.Labels {
		label := strings.TrimSpace(raw)
		if label == "" {
			continue
		}
		if len(label) > maxSnapshotLabelLength {
			return nil, fmt.Errorf("snapshot label %q exceeds maximum of %d characters", label, maxSnapshotLabelLength)
		}
		key := strings.ToLower(label)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		labels = append(labels, label)
	}
	return labels, nil
}

func (e *Engine) ensureManageableSnapshot(snapshotID [32]byte) (SnapshotLifecycle, error) {
	_, found, err := e.idx.GetSnapshot(snapshotID)
	if err != nil {
		return SnapshotLifecycle{}, err
	}
	if !found {
		return SnapshotLifecycle{}, fmt.Errorf("snapshot not found")
	}
	return e.readSnapshotLifecycle(snapshotID)
}

func (e *Engine) readSnapshotLifecycle(snapshotID [32]byte) (SnapshotLifecycle, error) {
	raw, found, err := e.idx.GetSnapshotLifecycle(snapshotID)
	if err != nil || !found {
		return defaultSnapshotLifecycle(), err
	}
	var envelope manifest.SnapshotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return SnapshotLifecycle{}, fmt.Errorf("decode snapshot lifecycle envelope: %w", err)
	}
	plain, err := e.km.DecryptMetadata(envelope.Ciphertext, envelope.Nonce)
	if err != nil {
		return SnapshotLifecycle{}, fmt.Errorf("decrypt snapshot lifecycle: %w", err)
	}
	var lifecycle SnapshotLifecycle
	if err := json.Unmarshal(plain, &lifecycle); err != nil {
		return SnapshotLifecycle{}, fmt.Errorf("decode snapshot lifecycle: %w", err)
	}
	if lifecycle.Version != SnapshotLifecycleVersion {
		return SnapshotLifecycle{}, fmt.Errorf("unsupported snapshot lifecycle version %d", lifecycle.Version)
	}
	if lifecycle.State != SnapshotActive && lifecycle.State != SnapshotTrashed {
		return SnapshotLifecycle{}, fmt.Errorf("invalid snapshot lifecycle state %q", lifecycle.State)
	}
	return lifecycle, nil
}

func (e *Engine) writeSnapshotLifecycle(snapshotID [32]byte, lifecycle SnapshotLifecycle) error {
	lifecycle.Version = SnapshotLifecycleVersion
	plain, err := json.Marshal(lifecycle)
	if err != nil {
		return err
	}
	ciphertext, nonce, err := e.km.EncryptMetadata(plain)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(manifest.SnapshotEnvelope{Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return err
	}
	return e.idx.PutSnapshotLifecycle(snapshotID, raw)
}
