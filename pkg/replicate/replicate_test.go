package replicate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// fakeStore is a minimal in-memory pack.StorageEngine for replication tests.
type fakeStore struct {
	mu       sync.Mutex
	items    map[[32]byte][]byte
	putFails int // number of subsequent PutPack calls to fail before succeeding
}

func newFakeStore() *fakeStore {
	return &fakeStore{items: make(map[[32]byte][]byte)}
}

func (s *fakeStore) PutPack(_ context.Context, id [32]byte, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putFails > 0 {
		s.putFails--
		return errors.New("simulated transient failure")
	}
	s.items[id] = data
	return nil
}

func (s *fakeStore) GetPack(_ context.Context, id [32]byte) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.items[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeStore) GetChunkRange(context.Context, [32]byte, int64, int64) ([]byte, error) {
	return nil, errors.New("not supported")
}

func (s *fakeStore) DeletePack(_ context.Context, id [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return nil
}

func (s *fakeStore) ListPacks(context.Context) ([][32]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([][32]byte, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	return ids, nil
}

func TestDaemonRunReplicatesMissingItemsAndSkipsExisting(t *testing.T) {
	source := newFakeStore()
	dest := newFakeStore()

	var idA, idB [32]byte
	idA[0], idB[0] = 1, 2
	source.items[idA] = []byte("payload-a")
	source.items[idB] = []byte("payload-b")
	dest.items[idA] = []byte("payload-a") // already replicated

	daemon := NewDaemon(source, dest)
	var events []Event
	result, err := daemon.Run(context.Background(), func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ItemsReplicated != 1 || result.ItemsSkipped != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.BytesReplicated != int64(len("payload-b")) {
		t.Fatalf("unexpected bytes replicated: %d", result.BytesReplicated)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 progress events, got %d", len(events))
	}
	if string(dest.items[idB]) != "payload-b" {
		t.Fatalf("expected item B copied to destination")
	}
}

func TestDaemonRunIsIdempotent(t *testing.T) {
	source := newFakeStore()
	dest := newFakeStore()
	var id [32]byte
	id[0] = 7
	source.items[id] = []byte("content")

	daemon := NewDaemon(source, dest)
	if _, err := daemon.Run(context.Background(), nil); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	result, err := daemon.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if result.ItemsReplicated != 0 || result.ItemsSkipped != 1 {
		t.Fatalf("expected second run to skip already-replicated item, got %+v", result)
	}
}

func TestDaemonRunRetriesTransientFailures(t *testing.T) {
	source := newFakeStore()
	dest := newFakeStore()
	var id [32]byte
	id[0] = 3
	source.items[id] = []byte("retry-me")
	dest.putFails = 2 // fail twice, succeed on the 3rd attempt

	daemon := NewDaemon(source, dest)
	daemon.RetryBackoff = time.Millisecond
	result, err := daemon.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ItemsReplicated != 1 {
		t.Fatalf("expected item to eventually replicate, got %+v", result)
	}
}

func TestDaemonRunFailsAfterExhaustingRetries(t *testing.T) {
	source := newFakeStore()
	dest := newFakeStore()
	var id [32]byte
	id[0] = 4
	source.items[id] = []byte("always-fails")
	dest.putFails = 10

	daemon := NewDaemon(source, dest)
	daemon.MaxAttempts = 2
	daemon.RetryBackoff = time.Millisecond
	if _, err := daemon.Run(context.Background(), nil); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestDaemonRunHonorsBandwidthLimit(t *testing.T) {
	source := newFakeStore()
	dest := newFakeStore()
	var id [32]byte
	id[0] = 5
	payload := bytes.Repeat([]byte{0xAA}, 64*1024)
	source.items[id] = payload

	daemon := NewDaemon(source, dest)
	daemon.BytesPerSecond = 32 * 1024

	start := time.Now()
	result, err := daemon.Run(context.Background(), nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ItemsReplicated != 1 {
		t.Fatalf("expected 1 item replicated, got %+v", result)
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("expected bandwidth limiting to slow the transfer, took %s", elapsed)
	}
	if !bytes.Equal(dest.items[id], payload) {
		t.Fatal("replicated payload does not match source")
	}
}
