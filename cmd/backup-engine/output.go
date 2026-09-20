package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

type outputOptions struct {
	verbose bool
	quiet   bool
	json    bool
}

func bindOutputOptions(flags *flag.FlagSet) *outputOptions {
	options := &outputOptions{}
	flags.BoolVar(&options.verbose, "verbose", false, "show operation phase progress and detailed summary")
	flags.BoolVar(&options.quiet, "quiet", false, "show only the primary result")
	flags.BoolVar(&options.json, "json", false, "write the final result as JSON")
	return options
}

func (options outputOptions) reporter() pipeline.Reporter {
	if options.quiet || options.json || (!options.verbose && !isatty.IsTerminal(os.Stderr.Fd())) {
		return nil
	}
	return func(event pipeline.ProgressEvent) {
		label := strings.ToUpper(string(event.Phase))
		if event.Total > 0 {
			fmt.Fprintf(os.Stderr, "%-11s %s (%d/%d %s)\n", label, event.Message, event.Completed, event.Total, event.Unit)
			return
		}
		fmt.Fprintf(os.Stderr, "%-11s %s\n", label, event.Message)
	}
}

func (options outputOptions) rich() bool {
	return !options.quiet && !options.json && (options.verbose || isatty.IsTerminal(os.Stdout.Fd()))
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func backupJSON(result pipeline.BackupResult) map[string]any {
	skippedItems := make([]map[string]any, len(result.SkippedItems))
	for i, item := range result.SkippedItems {
		skippedItems[i] = map[string]any{
			"path":      item.Path,
			"kind":      item.Kind,
			"operation": item.Operation,
			"reason":    item.Reason,
		}
	}
	return map[string]any{
		"snapshot_id":     fmt.Sprintf("%x", result.SnapshotID),
		"files_scanned":   result.FilesScanned,
		"files_processed": result.FilesProcessed,
		"files_skipped":   result.FilesSkipped,
		"skipped_bytes":   result.SkippedBytes,
		"skipped_items":   skippedItems,
		"workers":         result.Workers,
		"logical_bytes":   result.LogicalBytes,
		"chunks_examined": result.ChunksExamined,
		"chunks_new":      result.ChunksNew,
		"chunks_reused":   result.ChunksReused,
		"packs_written":   result.PacksWritten,
		"stored_bytes":    result.StoredBytes,
		"retention_tags":  result.RetentionTags,
		"duration_ms":     result.Duration.Milliseconds(),
	}
}

func restoreJSON(result pipeline.RestoreResult) map[string]any {
	return map[string]any{
		"snapshot_id":    fmt.Sprintf("%x", result.SnapshotID),
		"destination":    result.Destination,
		"files_restored": result.FilesRestored,
		"logical_bytes":  result.LogicalBytes,
		"chunks_read":    result.ChunksRead,
		"duration_ms":    result.Duration.Milliseconds(),
	}
}

func verifyJSON(result pipeline.VerifyResult) map[string]any {
	return map[string]any{
		"snapshots_checked": result.SnapshotsChecked,
		"files_checked":     result.FilesChecked,
		"chunks_checked":    result.ChunksChecked,
		"logical_bytes":     result.LogicalBytes,
		"duration_ms":       result.Duration.Milliseconds(),
	}
}

func doctorJSON(result pipeline.DoctorResult) map[string]any {
	return map[string]any{
		"chunk_records_checked": result.ChunkRecordsChecked,
		"issues":                result.Issues,
		"verify":                verifyJSON(result.Verify),
		"duration_ms":           result.Duration.Milliseconds(),
	}
}

func gcJSON(result pipeline.GCResult) map[string]any {
	decisions := make([]map[string]any, 0, len(result.Decisions))
	for _, decision := range result.Decisions {
		decisions = append(decisions, map[string]any{"snapshot_id": fmt.Sprintf("%x", decision.SnapshotID), "keep": decision.Keep, "reason": decision.Reason})
	}
	return map[string]any{
		"snapshots_evaluated": result.SnapshotsEvaluated,
		"snapshots_retained":  result.SnapshotsRetained,
		"snapshots_dropped":   result.SnapshotsDropped,
		"chunks_examined":     result.ChunksExamined,
		"chunks_purged":       result.ChunksPurged,
		"decisions":           decisions,
		"duration_ms":         result.Duration.Milliseconds(),
	}
}

func hardDeleteJSON(result pipeline.HardDeleteResult) map[string]any {
	return map[string]any{
		"snapshot_id":      fmt.Sprintf("%x", result.SnapshotID),
		"chunks_reclaimed": result.ChunksReclaimed,
		"bytes_reclaimed":  result.BytesReclaimed,
		"duration_ms":      result.Duration.Milliseconds(),
	}
}
func snapshotDetailsJSON(detail pipeline.SnapshotDetails) map[string]any {
	createdUTC := ""
	createdLocal := ""
	if !detail.Timestamp.IsZero() {
		createdUTC = detail.Timestamp.UTC().Format(time.RFC3339)
		createdLocal = detail.Timestamp.Local().Format(time.RFC3339)
	}
	parentID := ""
	if detail.ParentSnapshotID != nil {
		parentID = fmt.Sprintf("%x", *detail.ParentSnapshotID)
	}
	return map[string]any{
		"snapshot_id":    fmt.Sprintf("%x", detail.ID),
		"readable":       detail.Readable,
		"status":         detail.StatusText,
		"created_utc":    createdUTC,
		"created_local":  createdLocal,
		"total_files":    detail.TotalFiles,
		"total_bytes":    detail.TotalBytes,
		"retention_tags": detail.RetentionTags,
		"parent_id":      parentID,
		"state":          detail.Lifecycle.State,
		"pinned":         detail.Lifecycle.Pinned,
		"retain_until":   detail.Lifecycle.RetainUntil,
		"labels":         detail.Lifecycle.Labels,
		"note":           detail.Lifecycle.Note,
		"trashed_at":     detail.Lifecycle.TrashedAt,
		"purge_after":    detail.Lifecycle.PurgeAfter,
	}
}

func replicationCountsJSON(counts pipeline.ReplicationCounts) map[string]any {
	return map[string]any{
		"items_replicated": counts.ItemsReplicated,
		"items_skipped":    counts.ItemsSkipped,
		"bytes_replicated": counts.BytesReplicated,
	}
}

func replicateJSON(result pipeline.ReplicationResult) map[string]any {
	return map[string]any{
		"packs":           replicationCountsJSON(result.Packs),
		"manifests":       replicationCountsJSON(result.Manifests),
		"cids_replicated": result.CIDsReplicated,
		"duration_ms":     result.Duration.Milliseconds(),
	}
}

func recoverJSON(result pipeline.RecoverResult) map[string]any {
	return map[string]any{
		"manifests_recovered": result.ManifestsRecovered,
		"packs_scanned":       result.PacksScanned,
		"chunk_locations":     result.ChunkLocations,
		"cid_directory_found": result.CIDDirectoryFound,
		"cids_recovered":      result.CIDsRecovered,
		"verify":              verifyJSON(result.Verify),
		"duration_ms":         result.Duration.Milliseconds(),
	}
}

func scrubJSON(result pipeline.ScrubResult) map[string]any {
	return map[string]any{
		"packs_scanned": result.PacksScanned,
		"packs_corrupt": result.PacksCorrupt,
		"issues":        result.Issues,
		"duration_ms":   result.Duration.Milliseconds(),
	}
}

func transactionLogEntryJSON(entry pipeline.TransactionLogEntry) map[string]any {
	return map[string]any{
		"operation":    entry.Operation,
		"status":       entry.Status,
		"started_at":   entry.StartedAt.UTC().Format(time.RFC3339),
		"completed_at": entry.CompletedAt.UTC().Format(time.RFC3339),
		"duration_ms":  entry.Duration.Milliseconds(),
		"result":       entry.Result,
		"error":        entry.Error,
	}
}

func printSummary(title string, rows ...string) {
	fmt.Println()
	fmt.Println(title)
	for _, row := range rows {
		fmt.Println("  " + row)
	}
}

func printOperationError(err error) {
	presentation := pipeline.PresentError(err)
	fmt.Fprintln(os.Stderr, presentation.Summary+": "+presentation.Cause)
	fmt.Fprintln(os.Stderr, "Next step:", presentation.Hint)
}
