package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

func runRecover(args []string) error {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	output := bindOutputOptions(fs)
	remoteOpts := bindRemoteStorageOptions(fs)
	repo := fs.String("repo", "", "fresh/empty repository directory to rebuild into")
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
	result, err := pipeline.RecoverFromRemoteDetailed(context.Background(), pipeline.RecoverConfig{
		RepoDir:    *repo,
		Passphrase: passphrase,
		Salt:       salt,
		Remote:     remoteOpts.config(),
	}, output.reporter())
	if err != nil {
		return err
	}
	if output.json {
		return writeJSON(recoverJSON(result))
	}
	fmt.Printf("recover: manifests +%d, packs %d, chunk locations %d\n", result.ManifestsRecovered, result.PacksScanned, result.ChunkLocations)
	if !result.CIDDirectoryFound {
		fmt.Println("warning: no remote CID directory found; chunks remain undecryptable until a full rebuild from original sources (run replicate at least once after upgrading to enable full disaster recovery)")
	}
	if output.rich() {
		printSummary("Recovery summary",
			fmt.Sprintf("Manifests recovered: %d", result.ManifestsRecovered),
			fmt.Sprintf("Packs scanned: %d", result.PacksScanned),
			fmt.Sprintf("Chunk locations recovered: %d", result.ChunkLocations),
			fmt.Sprintf("CID directory found/recovered: %v/%d", result.CIDDirectoryFound, result.CIDsRecovered),
			fmt.Sprintf("Snapshots verified: %d", result.Verify.SnapshotsChecked),
			fmt.Sprintf("Duration: %s", result.Duration.Round(time.Millisecond)),
		)
	}
	return nil
}
