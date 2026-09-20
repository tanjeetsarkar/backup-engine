package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

func runSnapshot(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("snapshot action is required: list, show, trash, untrash, pin, unpin, remove, or edit")
	}
	action := args[0]
	flags := flag.NewFlagSet("snapshot "+action, flag.ContinueOnError)
	output := bindOutputOptions(flags)
	storageOpts := bindStorageOptions(flags)
	repo := flags.String("repo", "", "repository directory")
	passphrase := flags.String("passphrase", "", "repository passphrase")
	salt := flags.String("salt", "", "repository salt (min 16 chars)")
	idValue := flags.String("id", "", "snapshot id (hex)")
	includeTrash := flags.Bool("include-trash", false, "include recoverably trashed snapshots")
	trashFor := flags.String("trash-for", "168h", "recovery window before trashed snapshot becomes purgeable")
	labels := flags.String("labels", "", "comma-separated encrypted labels")
	note := flags.String("note", "", "encrypted operator note")
	retainUntil := flags.String("retain-until", "", "retention deadline in RFC3339")
	clearRetainUntil := flags.Bool("clear-retain-until", false, "clear the existing retention deadline")
	confirmRemove := flags.Bool("yes", false, "confirm permanent, instant snapshot removal")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	provided := make(map[string]bool)
	flags.Visit(func(current *flag.Flag) { provided[current.Name] = true })

	storage, err := storageOpts.resolve(*repo)
	if err != nil {
		return err
	}
	engine, err := pipeline.Open(pipeline.EngineConfig{RepoDir: *repo, Passphrase: []byte(*passphrase), Salt: []byte(*salt), Storage: storage})
	if err != nil {
		return err
	}
	defer engine.Close()

	if action == "list" {
		details, err := engine.ListSnapshotDetails(*includeTrash)
		if err != nil {
			return err
		}
		if output.json {
			encoded := make([]map[string]any, 0, len(details))
			for _, detail := range details {
				encoded = append(encoded, snapshotDetailsJSON(detail))
			}
			return writeJSON(encoded)
		}
		for _, detail := range details {
			fmt.Println(snapshotListLine(detail))
		}
		return nil
	}

	snapshotID, err := parseHexID(*idValue)
	if err != nil {
		return err
	}
	if action == "show" {
		detail, err := engine.GetSnapshotDetails(snapshotID)
		if err != nil {
			return err
		}
		if output.json {
			return writeJSON(snapshotDetailsJSON(detail))
		}
		printSnapshotDetails(detail)
		return nil
	}

	if action == "remove" {
		if !*confirmRemove {
			return fmt.Errorf("snapshot remove permanently deletes data immediately; pass -yes to confirm")
		}
		result, err := engine.HardDeleteSnapshotDetailed(context.Background(), snapshotID, output.reporter())
		if err != nil {
			return err
		}
		if output.json {
			return writeJSON(hardDeleteJSON(result))
		}
		fmt.Printf("snapshot removed: %x\n", snapshotID)
		if output.rich() {
			printSummary("Removal Summary",
				fmt.Sprintf("Chunks reclaimed: %d", result.ChunksReclaimed),
				fmt.Sprintf("Bytes reclaimed: %s", humanize.Bytes(uint64(result.BytesReclaimed))),
			)
		}
		return nil
	}

	switch action {
	case "trash":
		duration, err := time.ParseDuration(*trashFor)
		if err != nil {
			return fmt.Errorf("invalid trash recovery duration: %w", err)
		}
		_, err = engine.TrashSnapshot(snapshotID, duration)
		if err != nil {
			return err
		}
	case "untrash":
		if _, err := engine.UntrashSnapshot(snapshotID); err != nil {
			return err
		}
	case "pin", "unpin":
		if _, err := engine.SetSnapshotPin(snapshotID, action == "pin"); err != nil {
			return err
		}
	case "edit":
		current, err := engine.GetSnapshotDetails(snapshotID)
		if err != nil {
			return err
		}
		update := pipeline.SnapshotMetadataUpdate{Labels: current.Lifecycle.Labels, Note: current.Lifecycle.Note}
		if provided["labels"] {
			update.Labels = splitTagsAllowEmpty(*labels)
		}
		if provided["note"] {
			update.Note = *note
		}
		if _, err := engine.UpdateSnapshotMetadata(snapshotID, update); err != nil {
			return err
		}
		deadline := current.Lifecycle.RetainUntil
		if *clearRetainUntil {
			deadline = nil
		} else if provided["retain-until"] {
			parsed, err := time.Parse(time.RFC3339, *retainUntil)
			if err != nil {
				return fmt.Errorf("retain-until must be RFC3339: %w", err)
			}
			deadline = &parsed
		}
		if _, err := engine.SetSnapshotRetainUntil(snapshotID, deadline); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown snapshot action %q", action)
	}
	detail, err := engine.GetSnapshotDetails(snapshotID)
	if err != nil {
		return err
	}
	if output.json {
		return writeJSON(snapshotDetailsJSON(detail))
	}
	fmt.Printf("snapshot %s: %x\n", action, snapshotID)
	if output.rich() {
		printSnapshotDetails(detail)
	}
	return nil
}

func splitTagsAllowEmpty(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return splitTags(value)
}

func snapshotListLine(detail pipeline.SnapshotDetails) string {
	created := "date unavailable"
	if !detail.Timestamp.IsZero() {
		created = detail.Timestamp.Local().Format("2006-01-02 15:04 MST")
	}
	return fmt.Sprintf("%s\t%x\t%s\t%s\t%d files\t%s", created, detail.ID, detail.StatusText, detail.Lifecycle.State, detail.TotalFiles, humanize.IBytes(uint64(max(int64(0), detail.TotalBytes))))
}

func printSnapshotDetails(detail pipeline.SnapshotDetails) {
	createdLocal, createdUTC := "unavailable", "unavailable"
	if !detail.Timestamp.IsZero() {
		createdLocal = detail.Timestamp.Local().Format("2006-01-02 15:04 MST")
		createdUTC = detail.Timestamp.UTC().Format(time.RFC3339)
	}
	printSummary("Snapshot details",
		fmt.Sprintf("ID: %x", detail.ID),
		"Created local: "+createdLocal,
		"Created UTC: "+createdUTC,
		fmt.Sprintf("Status/state: %s/%s", detail.StatusText, detail.Lifecycle.State),
		fmt.Sprintf("Files/size: %d/%s", detail.TotalFiles, humanize.IBytes(uint64(max(int64(0), detail.TotalBytes)))),
		"Labels: "+strings.Join(detail.Lifecycle.Labels, ", "),
		"Note: "+detail.Lifecycle.Note,
	)
}
