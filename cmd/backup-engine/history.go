package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

func runHistory(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("history action is required: list or clear")
	}
	action := args[0]
	flags := flag.NewFlagSet("history "+action, flag.ContinueOnError)
	output := bindOutputOptions(flags)
	storageOpts := bindStorageOptions(flags)
	repo := flags.String("repo", "", "repository directory")
	creds := bindCredentialOptions(flags)
	limit := flags.Int("limit", 50, "maximum number of entries to list (0 = all)")
	before := flags.String("before", "", "clear entries completed before this RFC3339 timestamp")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	passphrase, err := creds.resolvePassphrase()
	if err != nil {
		return err
	}
	salt, err := creds.resolveSalt()
	if err != nil {
		return err
	}
	storage, err := storageOpts.resolve(*repo)
	if err != nil {
		return err
	}
	engine, err := pipeline.Open(pipeline.EngineConfig{RepoDir: *repo, Passphrase: passphrase, Salt: salt, Storage: storage})
	if err != nil {
		return err
	}
	defer engine.Close()

	switch action {
	case "list":
		entries, err := engine.ListTransactionHistory(*limit)
		if err != nil {
			return err
		}
		if output.json {
			encoded := make([]map[string]any, 0, len(entries))
			for _, entry := range entries {
				encoded = append(encoded, transactionLogEntryJSON(entry))
			}
			return writeJSON(encoded)
		}
		for _, entry := range entries {
			fmt.Printf("%s\t%-16s\t%-7s\t%s\n", entry.CompletedAt.Local().Format(time.RFC3339), entry.Operation, entry.Status, entry.Duration.Round(time.Millisecond))
			if entry.Status == "failed" && entry.Error != "" {
				fmt.Printf("\terror: %s\n", entry.Error)
			}
		}
		return nil
	case "clear":
		if *before == "" {
			return fmt.Errorf("history clear requires -before RFC3339 cutoff")
		}
		cutoff, err := time.Parse(time.RFC3339, *before)
		if err != nil {
			return fmt.Errorf("invalid -before timestamp: %w", err)
		}
		removed, err := engine.ClearTransactionHistoryBefore(cutoff)
		if err != nil {
			return err
		}
		if output.json {
			return writeJSON(map[string]any{"removed": removed})
		}
		fmt.Printf("history: removed %d entries completed before %s\n", removed, cutoff.Format(time.RFC3339))
		return nil
	default:
		return fmt.Errorf("unknown history action %q", action)
	}
}
