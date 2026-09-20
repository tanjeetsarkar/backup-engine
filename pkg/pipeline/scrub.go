package pipeline

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/pack"
)

// Scrub re-reads and re-validates every packfile's own BLAKE3 trailer checksum, without
// decrypting any chunk payload. Unlike Verify (which only checks chunks referenced by a live
// snapshot), Scrub detects bit-rot on all data at rest, including packs no snapshot references
// yet or anymore. It works identically against local or remote storage since it is built on the
// pack.StorageEngine interface.
func (e *Engine) Scrub(ctx context.Context) error {
	result, err := e.ScrubDetailed(ctx, nil)
	if err != nil {
		return err
	}
	if len(result.Issues) == 0 {
		return nil
	}
	return fmt.Errorf("scrub found %d corrupt pack(s)", result.PacksCorrupt)
}

// ScrubDetailed runs Scrub and reports aggregate progress and structured issues.
func (e *Engine) ScrubDetailed(ctx context.Context, reporter Reporter) (result ScrubResult, err error) {
	started := time.Now()
	defer func() { e.recordTransaction(OperationScrub, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()
	emit(reporter, ProgressEvent{Operation: OperationScrub, Phase: PhasePreparing, Level: EventInfo, Message: "Listing stored packfiles"})

	packIDs, err := e.storage.ListPacks(ctx)
	if err != nil {
		return result, err
	}
	total := int64(len(packIDs))
	emit(reporter, ProgressEvent{Operation: OperationScrub, Phase: PhaseChecking, Level: EventInfo, Message: "Scanning packfiles", Total: total, Unit: "packs"})

	for _, packID := range packIDs {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.PacksScanned++

		if scrubErr := e.scrubOnePack(ctx, packID); scrubErr != nil {
			result.PacksCorrupt++
			result.Issues = append(result.Issues, DoctorIssue{
				Code:     "pack-checksum-mismatch",
				Severity: "error",
				Resource: fmt.Sprintf("%x", packID),
				Summary:  "Packfile failed its trailer checksum",
				Detail:   scrubErr.Error(),
				Hint:     "Restore this pack from a replicated copy or another backup of the repository.",
			})
		}
		emit(reporter, ProgressEvent{Operation: OperationScrub, Phase: PhaseChecking, Level: EventInfo, Message: "Scanned packfiles", Completed: result.PacksScanned, Total: total, Unit: "packs"})
	}

	level := EventSuccess
	message := "Scrub completed"
	if result.PacksCorrupt > 0 {
		level = EventWarning
		message = "Scrub found corrupt packfiles"
	}
	emit(reporter, ProgressEvent{Operation: OperationScrub, Phase: PhaseComplete, Level: level, Message: message, Completed: result.PacksScanned, Total: total, Unit: "packs"})
	return result, nil
}

func (e *Engine) scrubOnePack(ctx context.Context, packID [32]byte) error {
	rc, err := e.storage.GetPack(ctx, packID)
	if err != nil {
		return fmt.Errorf("read pack: %w", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read pack contents: %w", err)
	}
	if _, err := pack.ParseTailIndex(data); err != nil {
		return err
	}
	return nil
}
