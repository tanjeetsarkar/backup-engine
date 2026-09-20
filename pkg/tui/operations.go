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

func openRepository(repository repositoryContext) (*pipeline.Engine, error) {
	return pipeline.Open(pipeline.EngineConfig{
		RepoDir:    expandHome(repository.repo),
		Passphrase: []byte(repository.passphrase),
		Salt:       []byte(repository.salt),
	})
}

func runAction(ctx context.Context, repository repositoryContext, form actionForm) tea.Cmd {
	values := form.values()
	kind := form.kind
	return func() tea.Msg {
		switch kind {
		case actionInit:
			bind := strings.EqualFold(values[0], "yes") || strings.EqualFold(values[0], "y")
			err := pipeline.InitRepository(pipeline.InitConfig{RepoDir: expandHome(repository.repo), Passphrase: []byte(repository.passphrase), Salt: []byte(repository.salt), BindExisting: bind})
			return taskResultMsg{title: "Initialization failed", detail: "Repository initialized and key-check metadata written.", err: err}
		}

		engine, err := openRepository(repository)
		if err != nil {
			return taskResultMsg{title: "Could not open repository", err: err}
		}
		defer engine.Close()

		switch kind {
		case actionBackup:
			snapshotID, err := engine.BackupPath(ctx, expandHome(values[0]), nil, splitTags(values[1]))
			return taskResultMsg{title: "Backup failed", detail: fmt.Sprintf("Backup complete. Snapshot %x", snapshotID), err: err}
		case actionRestore:
			snapshotID, err := parseHexID(values[0])
			if err == nil {
				err = engine.RestoreSnapshot(ctx, snapshotID, expandHome(values[1]))
			}
			return taskResultMsg{title: "Restore failed", detail: "Restore completed successfully.", err: err}
		case actionGC:
			daily, _ := strconv.Atoi(values[0])
			weekly, _ := strconv.Atoi(values[1])
			monthly, _ := strconv.Atoi(values[2])
			yearly, _ := strconv.Atoi(values[3])
			grace, _ := time.ParseDuration(values[4])
			purged, err := engine.RunGC(ctx, retention.GFSPolicy{KeepDaily: daily, KeepWeekly: weekly, KeepMonthly: monthly, KeepYearly: yearly}, grace)
			return taskResultMsg{title: "Garbage collection failed", detail: fmt.Sprintf("Garbage collection complete. Purged %d chunks.", purged), err: err}
		default:
			return taskResultMsg{title: "Unsupported action", err: fmt.Errorf("unsupported action")}
		}
	}
}

func loadSnapshots(repository repositoryContext) tea.Cmd {
	return func() tea.Msg {
		engine, err := openRepository(repository)
		if err != nil {
			return taskResultMsg{title: "Could not load snapshots", err: err}
		}
		defer engine.Close()
		snapshots, err := engine.ListSnapshotStatuses()
		return taskResultMsg{title: "Could not load snapshots", detail: fmt.Sprintf("Loaded %d snapshots.", len(snapshots)), snapshots: snapshots, err: err}
	}
}

func runHealth(ctx context.Context, repository repositoryContext) tea.Cmd {
	return func() tea.Msg {
		engine, err := openRepository(repository)
		if err != nil {
			return taskResultMsg{title: "Health check failed", err: err}
		}
		defer engine.Close()
		if err := engine.Verify(ctx); err != nil {
			return taskResultMsg{title: "Verification failed", err: err}
		}
		if err := engine.Doctor(ctx); err != nil {
			return taskResultMsg{title: "Doctor check failed", err: err}
		}
		return taskResultMsg{detail: "Repository verified. Index and stored data are healthy."}
	}
}
