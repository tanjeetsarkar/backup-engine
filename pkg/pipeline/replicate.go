package pipeline

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/tanjeetsarkar/backup-engine/pkg/replicate"
)

// Replicate copies packfiles and snapshot manifests not yet present in the remote backend
// described by remoteCfg. It never deletes anything locally or remotely, and can be run repeatedly
// (already-replicated items are skipped). Packs and manifests are stored under separate "packs"/
// "manifests" sub-prefixes of remoteCfg.Prefix within the same bucket so they cannot collide.
func (e *Engine) Replicate(ctx context.Context, remoteCfg StorageConfig) (ReplicationResult, error) {
	return e.ReplicateDetailed(ctx, remoteCfg, nil)
}

// ReplicateDetailed is Replicate with progress reporting.
func (e *Engine) ReplicateDetailed(ctx context.Context, remoteCfg StorageConfig, reporter Reporter) (result ReplicationResult, err error) {
	started := time.Now()
	defer func() { e.recordTransaction(OperationReplicate, started, err, result) }()
	defer func() { result.Duration = time.Since(started) }()

	if remoteCfg.Backend == "" || remoteCfg.Backend == StorageBackendLocal {
		return result, fmt.Errorf("replication remote backend must not be local")
	}

	remotePacks, err := NewStorageEngine("", withSubPrefix(remoteCfg, "packs"))
	if err != nil {
		return result, fmt.Errorf("configure remote pack storage: %w", err)
	}
	remoteManifests, err := NewStorageEngine("", withSubPrefix(remoteCfg, "manifests"))
	if err != nil {
		return result, fmt.Errorf("configure remote manifest storage: %w", err)
	}

	emit(reporter, ProgressEvent{Operation: OperationReplicate, Phase: PhaseWriting, Level: EventInfo, Message: "Replicating packfiles"})
	packDaemon := replicate.NewDaemon(e.storage, remotePacks)
	packResult, err := packDaemon.Run(ctx, replicateEventAdapter(reporter, "packfiles"))
	result.Packs = ReplicationCounts{ItemsReplicated: packResult.ItemsReplicated, ItemsSkipped: packResult.ItemsSkipped, BytesReplicated: packResult.BytesReplicated}
	if err != nil {
		return result, fmt.Errorf("replicate packfiles: %w", err)
	}

	emit(reporter, ProgressEvent{Operation: OperationReplicate, Phase: PhaseWriting, Level: EventInfo, Message: "Replicating snapshot manifests"})
	manifestDaemon := replicate.NewDaemon(newManifestSourceAdapter(e.idx), remoteManifests)
	manifestResult, err := manifestDaemon.Run(ctx, replicateEventAdapter(reporter, "manifests"))
	result.Manifests = ReplicationCounts{ItemsReplicated: manifestResult.ItemsReplicated, ItemsSkipped: manifestResult.ItemsSkipped, BytesReplicated: manifestResult.BytesReplicated}
	if err != nil {
		return result, fmt.Errorf("replicate manifests: %w", err)
	}

	remoteIndex, err := NewStorageEngine("", withSubPrefix(remoteCfg, "index"))
	if err != nil {
		return result, fmt.Errorf("configure remote index storage: %w", err)
	}
	emit(reporter, ProgressEvent{Operation: OperationReplicate, Phase: PhaseWriting, Level: EventInfo, Message: "Replicating CID directory"})
	cidCount, err := e.replicateCIDDirectory(ctx, remoteIndex)
	result.CIDsReplicated = cidCount
	if err != nil {
		return result, fmt.Errorf("replicate cid directory: %w", err)
	}

	emit(reporter, ProgressEvent{Operation: OperationReplicate, Phase: PhaseComplete, Level: EventSuccess, Message: "Replication completed"})
	return result, nil
}

func withSubPrefix(cfg StorageConfig, sub string) StorageConfig {
	cfg.Prefix = path.Join(cfg.Prefix, sub)
	return cfg
}

func replicateEventAdapter(reporter Reporter, unit string) replicate.Reporter {
	if reporter == nil {
		return nil
	}
	return func(event replicate.Event) {
		reporter(ProgressEvent{Operation: OperationReplicate, Phase: PhaseWriting, Level: EventInfo, Message: event.Message + " " + unit, Completed: event.Completed, Total: event.Total, Unit: unit})
	}
}
