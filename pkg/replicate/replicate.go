// Package replicate implements one-shot, one-directional synchronization of a
// pack.StorageEngine's contents (packfiles or, via an adapter, snapshot manifests) to another
// pack.StorageEngine, for the 3-2-1 local-to-offsite replication workflow.
package replicate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
	"golang.org/x/time/rate"
)

// SyncResult summarizes one Daemon.Run pass.
type SyncResult struct {
	ItemsReplicated int64
	ItemsSkipped    int64
	BytesReplicated int64
	Duration        time.Duration
}

// Event is a presentation-neutral progress update emitted during a sync pass.
type Event struct {
	Message   string
	Completed int64
	Total     int64
}

// Reporter receives progress events synchronously. Implementations should return quickly.
type Reporter func(Event)

// Daemon copies items present in Source but missing from Dest, identified by their 32-byte ID.
// It never deletes or overwrites anything in Dest, and never reads from Dest beyond ListPacks.
type Daemon struct {
	Source pack.StorageEngine
	Dest   pack.StorageEngine

	// MaxAttempts bounds retries per item (default 3). RetryBackoff is the base delay between
	// attempts, doubled on each subsequent retry (default 500ms).
	MaxAttempts  int
	RetryBackoff time.Duration

	// BytesPerSecond throttles upload bandwidth to Dest when > 0 (0 means unlimited).
	BytesPerSecond int
}

// NewDaemon creates a Daemon with default retry/backoff settings and no bandwidth limit.
func NewDaemon(source, dest pack.StorageEngine) *Daemon {
	return &Daemon{Source: source, Dest: dest, MaxAttempts: 3, RetryBackoff: 500 * time.Millisecond}
}

// Run performs one synchronization pass, returning as soon as ctx is cancelled or an item fails
// after exhausting retries.
func (d *Daemon) Run(ctx context.Context, reporter Reporter) (SyncResult, error) {
	started := time.Now()
	var result SyncResult
	defer func() { result.Duration = time.Since(started) }()

	sourceIDs, err := d.Source.ListPacks(ctx)
	if err != nil {
		return result, fmt.Errorf("list source items: %w", err)
	}
	destIDs, err := d.Dest.ListPacks(ctx)
	if err != nil {
		return result, fmt.Errorf("list destination items: %w", err)
	}
	existing := make(map[[32]byte]struct{}, len(destIDs))
	for _, id := range destIDs {
		existing[id] = struct{}{}
	}

	var limiter *rate.Limiter
	if d.BytesPerSecond > 0 {
		limiter = rate.NewLimiter(rate.Limit(d.BytesPerSecond), throttleBurstBytes)
	}

	total := int64(len(sourceIDs))
	for i, id := range sourceIDs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, ok := existing[id]; ok {
			result.ItemsSkipped++
			emit(reporter, Event{Message: "already replicated", Completed: int64(i + 1), Total: total})
			continue
		}
		n, err := d.copyOneWithRetry(ctx, id, limiter)
		if err != nil {
			return result, fmt.Errorf("replicate %x: %w", id, err)
		}
		result.ItemsReplicated++
		result.BytesReplicated += n
		emit(reporter, Event{Message: "replicated", Completed: int64(i + 1), Total: total})
	}
	return result, nil
}

func emit(reporter Reporter, event Event) {
	if reporter != nil {
		reporter(event)
	}
}

func (d *Daemon) copyOneWithRetry(ctx context.Context, id [32]byte, limiter *rate.Limiter) (int64, error) {
	maxAttempts := d.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	backoff := d.RetryBackoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(backoff * time.Duration(int64(1)<<uint(attempt-1))):
			}
		}
		n, err := d.copyOne(ctx, id, limiter)
		if err == nil {
			return n, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

func (d *Daemon) copyOne(ctx context.Context, id [32]byte, limiter *rate.Limiter) (int64, error) {
	src, err := d.Source.GetPack(ctx, id)
	if err != nil {
		return 0, err
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		return 0, err
	}

	var upload io.Reader = bytes.NewReader(data)
	if limiter != nil {
		upload = &throttledReader{ctx: ctx, r: upload, limiter: limiter}
	}
	if err := d.Dest.PutPack(ctx, id, upload, int64(len(data))); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

// throttleBurstBytes bounds each rate-limiter token request regardless of the configured rate, so
// WaitN never rejects a read chunk for exceeding the bucket's burst size.
const throttleBurstBytes = 32 * 1024

// throttledReader caps read throughput using a token-bucket limiter sized in bytes.
type throttledReader struct {
	ctx     context.Context
	r       io.Reader
	limiter *rate.Limiter
}

func (t *throttledReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	consumed := 0
	for consumed < n {
		chunk := n - consumed
		if chunk > throttleBurstBytes {
			chunk = throttleBurstBytes
		}
		if waitErr := t.limiter.WaitN(t.ctx, chunk); waitErr != nil {
			return consumed + chunk, waitErr
		}
		consumed += chunk
	}
	return n, err
}
