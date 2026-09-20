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
			fmt.Fprintln(os.Stderr, "backup failed:", err)
			os.Exit(1)
		}
	case "restore":
		if err := runRestore(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "restore failed:", err)
			os.Exit(1)
		}
	case "list-snapshots":
		if err := runListSnapshots(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "list-snapshots failed:", err)
			os.Exit(1)
		}
	case "gc":
		if err := runGC(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "gc failed:", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "verify failed:", err)
			os.Exit(1)
		}
	case "doctor":
		if err := runDoctor(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "doctor failed:", err)
			os.Exit(1)
		}
	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "init failed:", err)
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
	snapshotID, err := eng.BackupPath(context.Background(), *source, nil, normTags)
	if err != nil {
		return err
	}

	fmt.Printf("snapshot: %x\n", snapshotID)
	return nil
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
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

	return eng.RestoreSnapshot(context.Background(), snapshotID, *dest)
}

func runListSnapshots(args []string) error {
	fs := flag.NewFlagSet("list-snapshots", flag.ContinueOnError)
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

		for _, id := range ids {
			fmt.Printf("%x\n", id)
		}
		return nil
	}

	statuses, err := eng.ListSnapshotStatuses()
	if err != nil {
		return err
	}

	for _, s := range statuses {
		fmt.Printf("%x\t%s\n", s.ID, s.StatusText)
	}

	return nil
}

func runGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
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

	purged, err := eng.RunGC(context.Background(), retention.GFSPolicy{
		KeepDaily:   *keepDaily,
		KeepWeekly:  *keepWeekly,
		KeepMonthly: *keepMonthly,
		KeepYearly:  *keepYearly,
	}, graceDuration)
	if err != nil {
		return err
	}

	fmt.Println("purged-chunks:", purged)
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
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

	if err := eng.Verify(context.Background()); err != nil {
		return err
	}

	fmt.Println("verify: OK")
	return nil
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
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

	if err := eng.Doctor(context.Background()); err != nil {
		return err
	}

	fmt.Println("doctor: healthy")
	return nil
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	bindExisting := fs.Bool("bind-existing", false, "bind key-check for existing repository data")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := pipeline.InitRepository(pipeline.InitConfig{
		RepoDir:      *repo,
		Passphrase:   []byte(*passphrase),
		Salt:         []byte(*salt),
		BindExisting: *bindExisting,
	}); err != nil {
		return err
	}

	fmt.Println("init: OK")
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
