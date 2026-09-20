package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

func runScrub(args []string) error {
	fs := flag.NewFlagSet("scrub", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	storageOpts := bindStorageOptions(fs)
	repo := fs.String("repo", "", "repository directory")
	creds := bindCredentialOptions(fs)
	if err := fs.Parse(args); err != nil {
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
	eng, err := pipeline.Open(pipeline.EngineConfig{
		RepoDir:    *repo,
		Passphrase: passphrase,
		Salt:       salt,
		Storage:    storage,
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	result, err := eng.ScrubDetailed(context.Background(), output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		if err := writeJSON(scrubJSON(result)); err != nil {
			return err
		}
	} else {
		if len(result.Issues) == 0 {
			fmt.Println("scrub: healthy")
		} else {
			fmt.Printf("scrub: %d corrupt pack(s) found\n", result.PacksCorrupt)
			for _, issue := range result.Issues {
				fmt.Printf("- %s: %s\n  next: %s\n", issue.Code, issue.Summary, issue.Hint)
			}
		}
		if output.rich() {
			printSummary("Scrub summary",
				fmt.Sprintf("Packs scanned: %d", result.PacksScanned),
				fmt.Sprintf("Packs corrupt: %d", result.PacksCorrupt),
				fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
			)
		}
	}
	if result.PacksCorrupt > 0 {
		return fmt.Errorf("scrub found %d corrupt pack(s)", result.PacksCorrupt)
	}
	return nil
}
