package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
	"github.com/tanjeetsarkar/backup-engine/pkg/retention"
	"github.com/tanjeetsarkar/backup-engine/pkg/tui"
)

func main() {
	if len(os.Args) < 2 {
		if isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()) {
			if err := tui.Run(context.Background()); err != nil {
				fmt.Fprintln(os.Stderr, "tui failed:", err)
				os.Exit(1)
			}
			return
		}
		printUsage()
		os.Exit(2)
	}

	sub := os.Args[1]
	switch sub {
	case "backup":
		if err := runBackup(os.Args[2:]); err != nil {
			printOperationError(err)
			os.Exit(1)
		}
	case "restore":
		if err := runRestore(os.Args[2:]); err != nil {
			printOperationError(err)
			os.Exit(1)
		}
	case "list-snapshots":
		if err := runListSnapshots(os.Args[2:]); err != nil {
			printOperationError(err)
			os.Exit(1)
		}
	case "gc":
		if err := runGC(os.Args[2:]); err != nil {
			printOperationError(err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(os.Args[2:]); err != nil {
			printOperationError(err)
			os.Exit(1)
		}
	case "doctor":
		if err := runDoctor(os.Args[2:]); err != nil {
			printOperationError(err)
			os.Exit(1)
		}
	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			printOperationError(err)
			os.Exit(1)
		}
	case "tui":
		if err := tui.Run(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "tui failed:", err)
			os.Exit(1)
		}
	default:
		printUsage()
		os.Exit(2)
	}
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	source := fs.String("source", "", "source file or directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	tags := fs.String("tags", "DAILY", "comma-separated retention tags")
	if err := fs.Parse(args); err != nil {
		return err
	}

	eng, err := pipeline.Open(pipeline.EngineConfig{
		RepoDir:    *repo,
		Passphrase: []byte(*passphrase),
		Salt:       []byte(*salt),
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	normTags := splitTags(*tags)
	result, err := eng.BackupPathDetailed(context.Background(), *source, nil, normTags, output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		return writeJSON(backupJSON(result))
	}

	fmt.Printf("snapshot: %x\n", result.SnapshotID)
	if output.rich() {
		printSummary("Backup summary",
			fmt.Sprintf("Files: %d", result.FilesProcessed),
			fmt.Sprintf("Logical bytes: %d", result.LogicalBytes),
			fmt.Sprintf("Chunks new/reused: %d/%d", result.ChunksNew, result.ChunksReused),
			fmt.Sprintf("Packs written: %d", result.PacksWritten),
			fmt.Sprintf("Stored bytes: %d", result.StoredBytes),
			fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
		)
	}
	return nil
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	dest := fs.String("dest", "", "restore destination root")
	snapshot := fs.String("snapshot", "", "snapshot id (hex)")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	snapshotID, err := parseHexID(*snapshot)
	if err != nil {
		return err
	}

	eng, err := pipeline.Open(pipeline.EngineConfig{
		RepoDir:    *repo,
		Passphrase: []byte(*passphrase),
		Salt:       []byte(*salt),
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	result, err := eng.RestoreSnapshotDetailed(context.Background(), snapshotID, *dest, output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		return writeJSON(restoreJSON(result))
	}
	if output.rich() {
		printSummary("Restore summary",
			fmt.Sprintf("Files restored: %d", result.FilesRestored),
			fmt.Sprintf("Logical bytes: %d", result.LogicalBytes),
			fmt.Sprintf("Chunks read: %d", result.ChunksRead),
			fmt.Sprintf("Destination: %s", result.Destination),
			fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
		)
	}
	return nil
}

func runListSnapshots(args []string) error {
	fs := flag.NewFlagSet("list-snapshots", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	validate := fs.Bool("validate", true, "validate snapshot readability with current key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	eng, err := pipeline.Open(pipeline.EngineConfig{
		RepoDir:    *repo,
		Passphrase: []byte(*passphrase),
		Salt:       []byte(*salt),
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	if !*validate {
		ids, err := eng.ListSnapshots()
		if err != nil {
			return err
		}

		if output.json {
			encoded := make([]string, len(ids))
			for index, id := range ids {
				encoded[index] = fmt.Sprintf("%x", id)
			}
			return writeJSON(encoded)
		}
		for _, id := range ids {
			fmt.Printf("%x\n", id)
		}
		return nil
	}

	statuses, err := eng.ListSnapshotStatuses()
	if err != nil {
		return err
	}

	if output.json {
		encoded := make([]map[string]any, 0, len(statuses))
		for _, status := range statuses {
			encoded = append(encoded, map[string]any{"snapshot_id": fmt.Sprintf("%x", status.ID), "readable": status.Readable, "status": status.StatusText})
		}
		return writeJSON(encoded)
	}
	for _, s := range statuses {
		fmt.Printf("%x\t%s\n", s.ID, s.StatusText)
	}

	return nil
}

func runGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	keepDaily := fs.Int("keep-daily", 7, "retain daily snapshots")
	keepWeekly := fs.Int("keep-weekly", 4, "retain weekly snapshots")
	keepMonthly := fs.Int("keep-monthly", 12, "retain monthly snapshots")
	keepYearly := fs.Int("keep-yearly", 3, "retain yearly snapshots")
	grace := fs.String("grace", "24h", "minimum chunk grace period before purge")
	if err := fs.Parse(args); err != nil {
		return err
	}

	graceDuration, err := time.ParseDuration(*grace)
	if err != nil {
		return fmt.Errorf("invalid grace duration: %w", err)
	}

	eng, err := pipeline.Open(pipeline.EngineConfig{
		RepoDir:    *repo,
		Passphrase: []byte(*passphrase),
		Salt:       []byte(*salt),
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	result, err := eng.RunGCDetailed(context.Background(), retention.GFSPolicy{
		KeepDaily:   *keepDaily,
		KeepWeekly:  *keepWeekly,
		KeepMonthly: *keepMonthly,
		KeepYearly:  *keepYearly,
	}, graceDuration, output.reporter())
	if err != nil {
		return err
	}

	if output.json {
		return writeJSON(gcJSON(result))
	}
	fmt.Println("purged-chunks:", result.ChunksPurged)
	if output.rich() {
		printSummary("Garbage collection summary",
			fmt.Sprintf("Snapshots evaluated: %d", result.SnapshotsEvaluated),
			fmt.Sprintf("Snapshots retained/dropped: %d/%d", result.SnapshotsRetained, result.SnapshotsDropped),
			fmt.Sprintf("Chunks examined: %d", result.ChunksExamined),
			fmt.Sprintf("Chunks purged: %d", result.ChunksPurged),
			fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
		)
	}
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	eng, err := pipeline.Open(pipeline.EngineConfig{
		RepoDir:    *repo,
		Passphrase: []byte(*passphrase),
		Salt:       []byte(*salt),
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	result, err := eng.VerifyDetailed(context.Background(), output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		return writeJSON(verifyJSON(result))
	}

	fmt.Println("verify: OK")
	if output.rich() {
		printSummary("Verification summary",
			fmt.Sprintf("Snapshots checked: %d", result.SnapshotsChecked),
			fmt.Sprintf("Files checked: %d", result.FilesChecked),
			fmt.Sprintf("Chunks checked: %d", result.ChunksChecked),
			fmt.Sprintf("Logical bytes: %d", result.LogicalBytes),
			fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
		)
	}
	return nil
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	eng, err := pipeline.Open(pipeline.EngineConfig{
		RepoDir:    *repo,
		Passphrase: []byte(*passphrase),
		Salt:       []byte(*salt),
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	result, err := eng.DoctorDetailed(context.Background(), output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		if err := writeJSON(doctorJSON(result)); err != nil {
			return err
		}
	} else {
		if len(result.Issues) == 0 {
			fmt.Println("doctor: healthy")
		} else {
			fmt.Printf("doctor: %d issue(s) found\n", len(result.Issues))
			for _, issue := range result.Issues {
				fmt.Printf("- %s: %s\n  next: %s\n", issue.Code, issue.Summary, issue.Hint)
			}
		}
		if output.rich() {
			printSummary("Doctor summary",
				fmt.Sprintf("Chunk records checked: %d", result.ChunkRecordsChecked),
				fmt.Sprintf("Snapshots verified: %d", result.Verify.SnapshotsChecked),
				fmt.Sprintf("Issues: %d", len(result.Issues)),
				fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
			)
		}
	}
	if len(result.Issues) > 0 {
		return fmt.Errorf("repository health checks found %d issue(s)", len(result.Issues))
	}

	return nil
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	bindExisting := fs.Bool("bind-existing", false, "bind key-check for existing repository data")
	if err := fs.Parse(args); err != nil {
		return err
	}

	result, err := pipeline.InitRepositoryDetailed(pipeline.InitConfig{
		RepoDir:      *repo,
		Passphrase:   []byte(*passphrase),
		Salt:         []byte(*salt),
		BindExisting: *bindExisting,
	}, output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		return writeJSON(map[string]any{"repository": result.Repository, "bound_existing": result.BoundExisting, "key_check_ready": result.KeyCheckReady, "duration_ms": result.Duration.Milliseconds()})
	}

	fmt.Println("init: OK")
	if output.rich() {
		printSummary("Initialization summary", "Repository: "+result.Repository, fmt.Sprintf("Bound existing data: %t", result.BoundExisting), "Key-check metadata: ready", "Duration: "+result.Duration.Round(time.Millisecond).String())
	}
	return nil
}

func parseHexID(hexID string) ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(hexID)
	if err != nil {
		return out, fmt.Errorf("invalid snapshot id hex: %w", err)
	}
	if len(raw) != 32 {
		return out, fmt.Errorf("invalid snapshot id length: expected 32 bytes")
	}
	copy(out[:], raw)
	return out, nil
}

func splitTags(raw string) []string {
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		tags = append(tags, trimmed)
	}
	if len(tags) == 0 {
		return []string{"DAILY"}
	}
	return tags
}

func printUsage() {
	fmt.Println("backup-engine <command> [flags]")
	fmt.Println("commands: init, tui, backup, restore, list-snapshots, gc, verify, doctor")
	fmt.Println("init: backup-engine init -repo /repo -passphrase secret -salt 0123456789abcdef")
	fmt.Println("tui:  backup-engine tui")
	fmt.Println("example: backup-engine gc -repo /repo -passphrase secret -salt 0123456789abcdef -keep-daily 7")
	fmt.Println("hint: salts must be at least 16 bytes; use consistent passphrase/salt per repository")
}
