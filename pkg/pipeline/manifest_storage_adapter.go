package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/tanjeetsarkar/backup-engine/pkg/index"
	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
)

// manifestSourceAdapter exposes a repository's encrypted snapshot envelopes through the
// pack.StorageEngine interface (keyed by SnapshotID instead of PackID) so pkg/replicate.Daemon can
// replicate manifests using the same generic sync logic it uses for packfiles. It is read-only:
// PutPack and DeletePack are unsupported since manifest replication only ever flows local -> remote.
type manifestSourceAdapter struct {
	idx *index.DB
}

func newManifestSourceAdapter(idx *index.DB) pack.StorageEngine {
	return &manifestSourceAdapter{idx: idx}
}

func (m *manifestSourceAdapter) PutPack(context.Context, [32]byte, io.Reader, int64) error {
	return fmt.Errorf("manifest source is read-only")
}

func (m *manifestSourceAdapter) GetPack(_ context.Context, snapshotID [32]byte) (io.ReadCloser, error) {
	envelope, found, err := m.idx.GetSnapshot(snapshotID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(envelope)), nil
}

func (m *manifestSourceAdapter) GetChunkRange(context.Context, [32]byte, int64, int64) ([]byte, error) {
	return nil, fmt.Errorf("manifest source does not support range reads")
}

func (m *manifestSourceAdapter) DeletePack(context.Context, [32]byte) error {
	return fmt.Errorf("manifest source is read-only")
}

func (m *manifestSourceAdapter) ListPacks(context.Context) ([][32]byte, error) {
	return m.idx.ListSnapshotIDs()
}
