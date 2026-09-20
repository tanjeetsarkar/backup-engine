package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
	"github.com/tanjeetsarkar/backup-engine/pkg/retention"
)

type progressEventMsg struct{ event pipeline.ProgressEvent }
type progressStreamClosedMsg struct{}

func waitForProgress(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		message, ok := <-events
		if !ok {
			return progressStreamClosedMsg{}
		}
		return message
	}
}

func openRepository(repository repositoryContext) (*pipeline.Engine, error) {
	return pipeline.Open(pipeline.EngineConfig{
		RepoDir:    expandHome(repository.repo),
		Passphrase: []byte(repository.passphrase),
		Salt:       []byte(repository.salt),
	})
}

func runAction(ctx context.Context, repository repositoryContext, form actionForm, events chan tea.Msg) tea.Cmd {
	values := form.values()
	kind := form.kind
	return func() tea.Msg {
		defer close(events)
		reporter := func(event pipeline.ProgressEvent) { events <- progressEventMsg{event: event} }
		result := func() taskResultMsg {
			switch kind {
			case actionInit:
				bind := strings.EqualFold(values[0], "yes") || strings.EqualFold(values[0], "y")
				result, err := pipeline.InitRepositoryDetailed(pipeline.InitConfig{RepoDir: expandHome(repository.repo), Passphrase: []byte(repository.passphrase), Salt: []byte(repository.salt), BindExisting: bind}, reporter)
				return taskResultMsg{title: "Repository initialization", detail: "Repository initialized and key-check metadata written.", summary: []string{"Key-check metadata  ready", fmt.Sprintf("Bound existing data  %t", result.BoundExisting), fmt.Sprintf("Duration             %s", result.Duration.Round(time.Millisecond))}, next: "Connect the repository, then create a backup.", err: err}
			}

			engine, err := openRepository(repository)
			if err != nil {
				return taskResultMsg{title: "Could not open repository", err: err}
			}
			defer engine.Close()

			switch kind {
			case actionBackup:
				result, err := engine.BackupPathDetailed(ctx, expandHome(values[0]), nil, splitTags(values[1]), reporter)
				return taskResultMsg{
					title:  "Backup transaction",
					detail: fmt.Sprintf("Snapshot %x", result.SnapshotID),
					summary: []string{
						fmt.Sprintf("Files             %d", result.FilesProcessed),
						fmt.Sprintf("Logical bytes     %d", result.LogicalBytes),
						fmt.Sprintf("Chunks new/reused %d / %d", result.ChunksNew, result.ChunksReused),
						fmt.Sprintf("Packs written     %d", result.PacksWritten),
						fmt.Sprintf("Stored bytes      %d", result.StoredBytes),
						fmt.Sprintf("Duration          %s", result.Duration.Round(time.Millisecond)),
					},
					next: "Open Snapshots to inspect the new snapshot, then run Health to verify it.",
					err:  err,
				}
			case actionRestore:
				snapshotID, err := parseHexID(values[0])
				var result pipeline.RestoreResult
				if err == nil {
					result, err = engine.RestoreSnapshotDetailed(ctx, snapshotID, expandHome(values[1]), reporter)
				}
				return taskResultMsg{title: "Restore transaction", detail: "Restore completed successfully.", summary: []string{
					fmt.Sprintf("Files restored  %d", result.FilesRestored),
					fmt.Sprintf("Logical bytes   %d", result.LogicalBytes),
					fmt.Sprintf("Chunks read     %d", result.ChunksRead),
					fmt.Sprintf("Duration        %s", result.Duration.Round(time.Millisecond)),
				}, next: "Inspect the restored files at the destination before replacing any originals.", err: err}
			case actionGC:
				daily, _ := strconv.Atoi(values[0])
				weekly, _ := strconv.Atoi(values[1])
				monthly, _ := strconv.Atoi(values[2])
				yearly, _ := strconv.Atoi(values[3])
				grace, _ := time.ParseDuration(values[4])
				result, err := engine.RunGCDetailed(ctx, retention.GFSPolicy{KeepDaily: daily, KeepWeekly: weekly, KeepMonthly: monthly, KeepYearly: yearly}, grace, reporter)
				return taskResultMsg{title: "Garbage collection transaction", detail: "Retention evaluation and garbage collection completed.", summary: []string{
					fmt.Sprintf("Snapshots evaluated %d", result.SnapshotsEvaluated),
					fmt.Sprintf("Snapshots kept/drop %d / %d", result.SnapshotsRetained, result.SnapshotsDropped),
					fmt.Sprintf("Chunks examined     %d", result.ChunksExamined),
					fmt.Sprintf("Chunks purged       %d", result.ChunksPurged),
					fmt.Sprintf("Duration            %s", result.Duration.Round(time.Millisecond)),
				}, next: "Run Health after garbage collection to verify repository consistency.", err: err}
			default:
				return taskResultMsg{title: "Unsupported action", err: fmt.Errorf("unsupported action")}
			}
		}()
		events <- result
		return nil
	}
}

func loadSnapshots(repository repositoryContext) tea.Cmd {
	return func() tea.Msg {
		engine, err := openRepository(repository)
		if err != nil {
			return taskResultMsg{title: "Could not load snapshots", err: err}
		}
		defer engine.Close()
		snapshots, err := engine.ListSnapshotDetails(true)
		return taskResultMsg{title: "Could not load snapshots", detail: fmt.Sprintf("Loaded %d snapshots.", len(snapshots)), snapshots: snapshots, err: err}
	}
}

func loadHistory(repository repositoryContext) tea.Cmd {
	return func() tea.Msg {
		engine, err := openRepository(repository)
		if err != nil {
			return taskResultMsg{title: "Could not load transaction history", err: err}
		}
		defer engine.Close()
		entries, err := engine.ListTransactionHistory(200)
		return taskResultMsg{title: "Could not load transaction history", detail: fmt.Sprintf("Loaded %d history entries.", len(entries)), history: entries, err: err}
	}
}

func mutateSnapshot(repository repositoryContext, selected pipeline.SnapshotDetails, action string, form snapshotEditForm) tea.Cmd {
	return func() tea.Msg {
		engine, err := openRepository(repository)
		if err != nil {
			return snapshotMutationMsg{err: err}
		}
		defer engine.Close()
		if action == "remove" {
			result, err := engine.HardDeleteSnapshotDetailed(context.Background(), selected.ID, nil)
			if err != nil {
				return snapshotMutationMsg{err: err}
			}
			message := fmt.Sprintf("Snapshot removed instantly. Reclaimed %d chunks (%d bytes).", result.ChunksReclaimed, result.BytesReclaimed)
			return snapshotMutationMsg{detail: pipeline.SnapshotDetails{ID: selected.ID}, removed: true, title: "Snapshot removed", message: message}
		}
		switch action {
		case "trash":
			_, err = engine.TrashSnapshot(selected.ID, pipeline.DefaultTrashRetention)
		case "untrash":
			_, err = engine.UntrashSnapshot(selected.ID)
		case "pin":
			_, err = engine.SetSnapshotPin(selected.ID, !selected.Lifecycle.Pinned)
		case "edit":
			var update pipeline.SnapshotMetadataUpdate
			var retainUntil *time.Time
			update, retainUntil, err = form.values()
			if err == nil {
				_, err = engine.UpdateSnapshotMetadata(selected.ID, update)
			}
			if err == nil {
				_, err = engine.SetSnapshotRetainUntil(selected.ID, retainUntil)
			}
		default:
			err = fmt.Errorf("unsupported snapshot action %q", action)
		}
		if err != nil {
			return snapshotMutationMsg{err: err}
		}
		detail, err := engine.GetSnapshotDetails(selected.ID)
		return snapshotMutationMsg{detail: detail, title: "Snapshot updated", message: snapshotActionMessage(action, detail), err: err}
	}
}

func snapshotActionMessage(action string, detail pipeline.SnapshotDetails) string {
	switch action {
	case "trash":
		if detail.Lifecycle.PurgeAfter != nil {
			return "Snapshot moved to recoverable trash until " + detail.Lifecycle.PurgeAfter.Local().Format("2006-01-02 15:04 MST") + "."
		}
		return "Snapshot moved to recoverable trash."
	case "untrash":
		return "Snapshot restored to the active catalog."
	case "pin":
		if detail.Lifecycle.Pinned {
			return "Snapshot pinned against GFS expiration."
		}
		return "Snapshot unpinned; normal retention policy applies."
	case "edit":
		return "Encrypted labels, note, and retention override were updated."
	default:
		return "Snapshot metadata updated."
	}
}

func runHealth(ctx context.Context, repository repositoryContext, events chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		defer close(events)
		reporter := func(event pipeline.ProgressEvent) { events <- progressEventMsg{event: event} }
		resultMessage := func() taskResultMsg {
			engine, err := openRepository(repository)
			if err != nil {
				return taskResultMsg{title: "Health check failed", err: err}
			}
			defer engine.Close()
			result, err := engine.DoctorDetailed(ctx, reporter)
			warning := len(result.Issues) > 0
			return taskResultMsg{title: "Repository health transaction", detail: "Repository health checks completed.", summary: []string{
				fmt.Sprintf("Chunk records checked %d", result.ChunkRecordsChecked),
				fmt.Sprintf("Snapshots verified    %d", result.Verify.SnapshotsChecked),
				fmt.Sprintf("Files verified        %d", result.Verify.FilesChecked),
				fmt.Sprintf("Chunks verified       %d", result.Verify.ChunksChecked),
				fmt.Sprintf("Issues found          %d", len(result.Issues)),
				fmt.Sprintf("Duration              %s", result.Duration.Round(time.Millisecond)),
			}, next: "Review reported issues before running retention or deleting repository copies.", warning: warning, err: err}
		}()
		events <- resultMessage
		return nil
	}
}
