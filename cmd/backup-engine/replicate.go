package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

func runReplicate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("replicate action is required: run")
	}
	action := args[0]
	if action != "run" {
		return fmt.Errorf("unknown replicate action %q", action)
	}

	fs := flag.NewFlagSet("replicate "+action, flag.ContinueOnError)
	output := bindOutputOptions(fs)
	storageOpts := bindStorageOptions(fs)
	remoteOpts := bindRemoteStorageOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	passphrase := fs.String("passphrase", "", "repository passphrase")
	salt := fs.String("salt", "", "repository salt (min 16 chars)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	storage, err := storageOpts.resolve(*repo)
	if err != nil {
		return err
	}
	engine, err := pipeline.Open(pipeline.EngineConfig{RepoDir: *repo, Passphrase: []byte(*passphrase), Salt: []byte(*salt), Storage: storage})
	if err != nil {
		return err
	}
	defer engine.Close()

	result, err := engine.ReplicateDetailed(context.Background(), remoteOpts.config(), output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		return writeJSON(replicateJSON(result))
	}
	fmt.Printf("replicate: packs +%d (skipped %d), manifests +%d (skipped %d)\n",
		result.Packs.ItemsReplicated, result.Packs.ItemsSkipped, result.Manifests.ItemsReplicated, result.Manifests.ItemsSkipped)
	if output.rich() {
		printSummary("Replication summary",
			fmt.Sprintf("Packs replicated/skipped: %d/%d", result.Packs.ItemsReplicated, result.Packs.ItemsSkipped),
			fmt.Sprintf("Pack bytes replicated: %d", result.Packs.BytesReplicated),
			fmt.Sprintf("Manifests replicated/skipped: %d/%d", result.Manifests.ItemsReplicated, result.Manifests.ItemsSkipped),
			fmt.Sprintf("Manifest bytes replicated: %d", result.Manifests.BytesReplicated),
			fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
		)
	}
	return nil
}
