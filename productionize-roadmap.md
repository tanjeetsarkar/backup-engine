# Plan: Productionize backup-engine

## Scope decisions (confirmed with user)
- Broad scope: remaining roadmap checklist items (project_roadmap.md) PLUS CI/CD, releases,
  observability, security hardening, cross-platform support.
- Priorities: user selected ALL of reliability/data-safety, performance, cloud/immutability,
  operability, security, portability as important - plan orders them by actual risk/foundational
  value (CI first since it gates everything else; reliability/DR next since it's the biggest real
  gap; portability last/documented-only per platform answer).
- Platform: Linux-only acceptable for v1. Cross-platform (macOS/Windows) work is explicitly
  deferred/out of scope for this pass - just document the constraint (xattr/flock/Lutimes code is
  golang.org/x/sys/unix-only).
- Deployment model: single-user/self-hosted CLI+TUI, invoked manually or via cron/systemd timers.
  No always-on daemon/service is in scope (so "scheduled replication daemon" and "background
  scrubbing daemon" from the roadmap become one-shot CLI commands meant to be cron-invoked, not
  in-process schedulers).

## Research findings
- No CI/CD config anywhere (no .github/workflows), no Dockerfile, no Makefile, no LICENSE file.
- No versioning: no `version` command, no build-time ldflags version injection.
- No structured/durable logging beyond the existing human-facing progress reporter
  (cmd/backup-engine/output.go) and the encrypted transaction-history feature already built.
- pkg/chunker/chunker.go `FastCDC.NextChunk` allocates a fresh `[]byte` per emitted chunk
  (`make([]byte, cutPoint)` / `make([]byte, available)`) - confirmed NOT zero-allocation. No
  `func Benchmark...` exists anywhere in the repo - the roadmap's ">1.5GB/s/core" target has never
  been measured.
- No Bloom filter anywhere (grep confirmed). New-chunk dedup lookups go straight to bbolt via
  `idx.GetStorageID(cid)` in `pkg/pipeline/engine.go backupOneFile`.
- Pack trailer uses BLAKE3 only (`pkg/pack/pack.go` `Finalize()`), no CRC32 (roadmap literally asks
  for both; BLAKE3 alone is already a strictly stronger integrity check, so CRC32 is a very
  low-value addition - candidate to explicitly deprioritize/skip with a documented rationale).
- **Critical reliability gap found during this planning pass, not on the existing roadmap**:
  disaster recovery. `replicate run` is one-directional (local -> remote) only. The bbolt index
  (`<repo>/index/index.db`) - which holds `snapshots`, `chunks` (StorageID->PackID/Offset/Length),
  `cids`/`cid_by_sid` (CID<->StorageID dedup mappings), lifecycle, and transaction history - is
  ALWAYS local, never replicated. If the local machine/disk is lost entirely, there is currently NO
  command to rebuild a working repository from just the offsite bucket + passphrase/salt, even
  though packs and manifests both exist remotely (replicated under separate `packs`/`manifests`
  prefixes, see pkg/pipeline/replicate.go). This directly blocks the roadmap's Phase 6 "Disaster
  Recovery Automation" item and is arguably the single most important gap for a real production
  posture on a system explicitly designed as a backup tool.
  - What CAN be rebuilt from remote pack contents alone: `chunks` bucket (StorageID/PackID/Offset/
    Length are stored in each pack's own plaintext tail index, per `pkg/pack/pack.go` trailer
    format - no decryption needed to read the tail index).
  - What CANNOT be trivially rebuilt: `cids`/`cid_by_sid` (StorageID = BLAKE3(HMAC(metaKey, CID)) is
    one-way; the original CID is not recoverable from StorageID without decrypting every chunk and
    recomputing BLAKE3 of the plaintext). Missing this bucket doesn't break restore/verify/doctor
    (those only need `chunks`+`snapshots`), it only breaks *future* backup-time dedup lookups until
    a slower rebuild pass re-derives CIDs by decrypting each chunk.
  - Snapshot manifests: already recoverable directly from the remote `manifests` prefix (each keyed
    by SnapshotID, see `newManifestSourceAdapter`/`Engine.Replicate`).
- Passphrase/salt are passed as plaintext CLI flags everywhere (`-passphrase`, `-salt`) - visible in
  shell history and to any local user via `ps`/`/proc/<pid>/cmdline`. This is a real secrets-handling
  gap for a "production" posture. No env-var or file-based credential input exists yet.
- No key-rotation/passphrase-change capability exists, and the current key hierarchy makes it
  inherently expensive: rotating the passphrase changes the Argon2id-derived master key, which
  changes both `K_data` (so every chunk's convergent key changes) and `K_meta` - a full
  decrypt+re-encrypt of the entire repository would be required. This is a design-level limitation,
  not a quick fix; flagged as a "Further Consideration" rather than a concrete plan step.
- `go.mod`: `go 1.27.1`, all deps already reasonably current (minio-go v7, protobuf v1.36, bbolt
  v1.4.3, x/crypto v0.57, x/sys v0.48, x/time v0.16). No known-vulnerable versions spotted by
  inspection, but no `govulncheck` has ever been run in CI (there is no CI).

## Plan Steps (phases run roughly in the listed order; Phase 0 unblocks safe iteration on the rest)

### Phase 0: CI/CD, versioning, licensing (foundational, low effort, do first)
1. Add `.github/workflows/ci.yml`: on push/PR run `gofmt -l .` (fail on output), `go vet ./...`,
   `go build ./...`, `go test ./...`, `go test -race ./pkg/pipeline ./pkg/tui ./pkg/index
   ./pkg/replicate ./pkg/retention ./pkg/manifest`, and `govulncheck ./...` (see Phase 2).
2. Add `.github/workflows/release.yml` (tag-triggered): cross-compile `linux/amd64` +
   `linux/arm64` binaries via `GOOS=linux GOARCH=... go build -ldflags "-X main.version=$TAG"`,
   publish checksums, attach to a GitHub Release.
3. Add a `version` subcommand (`cmd/backup-engine/version.go`) reading a `var version = "dev"`
   package variable overridable via `-ldflags -X main.version=...` at build time; wire into
   `printUsage()`/main.go dispatch. This is the biggest quick win for support/bug-report quality
   since there is currently zero version introspection.
4. Add a `LICENSE` file - **needs a decision from the user on which license** (MIT/Apache-2.0/etc.)
   before this step can be completed; flagged as a Further Consideration below.
5. Optional: a thin `Makefile` wrapping the commands already in README's "Development" section
   (`fmt`, `vet`, `test`, `build`) for convenience; not required, just nice-to-have.

### Phase 1: Reliability & Disaster Recovery (highest-value gap)
1. New `Engine` capability in a new `pkg/pipeline/recover_remote.go`: `RebuildFromRemoteDetailed(ctx,
   remoteCfg StorageConfig, opts RebuildOptions, reporter Reporter) (RebuildResult, error)` that,
   against a **fresh/empty** local repository:
   - Lists and pulls every object under `manifests/` from the remote, decrypts+re-encrypts (or just
     copies the envelope bytes as-is, since format is already the on-disk format) into the local
     `snapshots` bucket, and reconstructs default lifecycle entries for each.
   - Lists every pack under `packs/` from the remote, reads each pack's tail index directly (via a
     new small exported helper in `pkg/pack` that parses just the trailer+tail index without
     decrypting chunk payloads) and repopulates the local `chunks` bucket
     (`UpsertChunkLocation`) with `UploadTimeUnix = now` (so GC's grace-period guard behaves safely
     immediately after rebuild).
   - Optional `opts.RebuildDedupIndex bool`: a slower second pass that decrypts every chunk (needs
     the passphrase/salt-derived keys, already available) to recompute `CID = BLAKE3(plaintext)` and
     repopulate `cids`/`cid_by_sid`, restoring full backup-time dedup efficiency. Off by default
     (restore/verify/doctor work fully without it; only *new* backups after a DR event benefit).
   - Ends by running the existing `VerifyDetailed` internally and surfacing its result, so the
     operator gets an immediate confidence check that the rebuilt repository is sound.
2. CLI: new `backup-engine recover -repo <fresh-dir> -passphrase -salt -remote-storage-backend
   minio -remote-s3-* [-rebuild-dedup-index]`, modeled on `replicate run`'s remote-flag pattern
   (reuse `bindRemoteStorageOptions`). Require the local repo dir to be empty/uninitialized (fail
   fast with a clear error otherwise, to avoid clobbering a live repository).
3. New `backup-engine scrub` command (and `Engine.ScrubDetailed`): iterates every pack via
   `storage.ListPacks`, re-reads and re-validates each pack's own BLAKE3 trailer checksum (cheap,
   no decryption) - this is genuine "bit-rot on data at rest" detection distinct from `verify`
   (which only checks chunks *referenced by a live snapshot*, and requires decrypting each one).
   Works against local or remote (`-storage-backend`) equally since it's built on the same
   `pack.StorageEngine` interface. Report `ScrubResult{PacksScanned, PacksCorrupt, Issues
   []DoctorIssue}` reusing the existing `DoctorIssue` shape/CLI JSON conventions.
4. Fault-injection / chaos tests: a small `pkg/pipeline` (or new `pkg/chaostest`) helper
   `faultyStorage` wrapping a real `pack.StorageEngine`, configurable to fail the Nth call to
   `PutPack`/`GetPack`/`DeletePack` or return truncated/bit-flipped bytes from `GetPack`/
   `GetChunkRange`. New tests:
   - Kill mid-pack-upload (fail PutPack after N bytes read) during `BackupPathDetailed`; assert the
     existing cancellation/journal rollback (already built) leaves a consistent, `doctor`-clean
     repository.
   - Bit-flip one byte in a stored pack's ciphertext, then run `VerifyDetailed`/`DoctorDetailed`;
     assert the failure surfaces as a structured `DoctorIssue`/AEAD-auth error, not a panic or
     silent data loss.
   - Clock-drift test: reuse `retention.NewGarbageCollectorWithClock` with a backdated/forward-dated
     injected clock to prove the grace-period invariant holds even when `now` moves non-monotonically
     relative to `UploadTimeUnix`.
5. CI integration test (depends on Phase 0's CI): spin up a MinIO container (GitHub Actions service
   container) and run a scripted end-to-end cycle: init -> backup -> `replicate run` -> delete the
   local repo dir entirely -> `recover` -> `restore`/`verify` -> diff restored content against the
   original source tree. This is the closest thing to the roadmap's "headless CI/CD disaster
   recovery" item and should live as a `-tags integration` gated test or a dedicated CI job step.

### Phase 2: Security hardening
1. Secrets input: add `-passphrase-file`/`-salt-file` flags (read trimmed file contents) and
   `BACKUP_ENGINE_PASSPHRASE`/`BACKUP_ENGINE_SALT` env-var fallbacks across every CLI command
   (mirror the existing `-s3-access-key` env-fallback pattern in `cmd/backup-engine/storage.go`).
   Keep `-passphrase`/`-salt` flags working (backward compatible) but document them as the
   least-safe option (visible via `ps`/shell history) in README/usage.md.
2. Add `govulncheck ./...` to the Phase-0 CI workflow; document the remediation policy (bump the
   flagged dependency, re-run full gate) in a short CONTRIBUTING or SECURITY note.
3. Add Go native fuzz tests (`testing.F`) at the boundaries that parse bytes originating from
   storage/disk (i.e. semi-untrusted input if storage is ever compromised or corrupted):
   - `pack.DecodeChunkRecord` / packfile tail-index parsing.
   - `manifest.UnmarshalSnapshotWire` and the `consumeFileNode`/`consumeDirectoryNode`/`consumeXAttr`
     hand-rolled protowire parsers (these are the most bespoke, least battle-tested parsing code in
     the repo and the best fuzz-testing ROI).
   - `manifest.UnmarshalSnapshot` (legacy JSON path) for completeness.
   Wire `go test -fuzz=. -fuzztime=30s` as a periodic (not per-PR) CI job given fuzzing runtime cost.
4. Document (README "Security" section) the passphrase-rotation limitation identified above as a
   known, currently-unsupported operation rather than silently leaving it undiscoverable.

### Phase 3: Cloud/immutability (S3 Object Lock / WORM)
1. Extend `pkg/storage/minio/minio.go`'s `Storage` with an optional retention configuration (e.g.
   `RetentionMode miniosdk.RetentionMode` + `RetentionDuration time.Duration`, or a
   `func(packID [32]byte) *minio.PutObjectOptions.Retention` hook) so `PutPack` can set
   Object-Lock retention headers (`PutObjectOptions.Retention`, `.GovernanceBypass`) using
   `minio-go`'s existing Object Lock support - no need for a new dependency.
2. Extend `pipeline.StorageConfig` with `ObjectLockRetentionDays int` (0 = disabled), threaded
   through `NewStorageEngine`'s minio branch; derive a sensible per-repository default from the
   configured GFS policy's longest retention tier if not explicitly set (e.g.
   `max(KeepYearly years, ...)`), surfaced as a new `-s3-object-lock-days` / `-remote-...` CLI flag.
3. Add a preflight check (in `init`/`replicate run` when object-lock flags are set) that reads the
   bucket's Object Lock configuration via `minio-go`'s `GetBucketObjectLockConfig` and warns/errors
   if the bucket wasn't created with Object Lock enabled (it cannot be retrofitted - this is
   inherent to S3 Object Lock and must be surfaced clearly rather than silently no-op'ing).
4. Document that COMPLIANCE mode is irreversible even by the bucket owner/root credentials - this
   must be an explicit, informed operator choice, not a quiet default.

### Phase 4: Performance (benchmark first, then optimize with data)
1. Add `go test -bench` benchmarks before changing anything: `BenchmarkFastCDCThroughput` (chunker
   only, synthetic random + realistic-ish data), and a pipeline-level
   `BenchmarkBackupPathDetailed` (chunk+compress+encrypt+pack, local storage) to get a real,
   reproducible baseline against the roadmap's >1.5GB/s/core target. Wire as an informational
   (non-failing) CI step so regressions are visible over time without blocking merges on hardware
   variance.
2. Only then: reduce `FastCDC.NextChunk` allocations - e.g. let the caller supply a reusable
   `[]byte` buffer (or a `sync.Pool` of chunk buffers) instead of allocating fresh per chunk;
   re-benchmark to confirm improvement before/after.
3. Add the Bloom filter short-circuit: a small in-memory blocked-bloom-filter (hand-rolled or a
   tiny dependency) built from the `cids` bucket at `Engine.Open()` (or lazily on first backup),
   consulted before `idx.GetStorageID(cid)` in `backupOneFile` - only ever used to skip the bbolt
   lookup on a definite-miss; a possible-hit still falls through to the real lookup, so correctness
   is unaffected even with false positives. Benchmark large-repository backup (many pre-existing
   chunks) before/after to quantify the win.
4. Verify (don't assume) whether `minio-go`'s `PutObject` already internally chooses multipart
   upload for packs above its internal threshold - if so, the roadmap's "multipart streaming
   uploads" item may already be effectively satisfied by the SDK and can be marked done with a
   one-line note rather than requiring new code.
5. CRC32 trailer: explicitly deprioritize/skip with a documented rationale (BLAKE3 is already a
   cryptographically strong integrity check; adding CRC32 alongside it adds roadmap-literal
   compliance but negligible real corruption-detection value) unless the user specifically wants it
   for compatibility with an external tool that expects CRC32.

### Phase 5 (documented only, not implemented): Portability & always-on daemon mode
- Record in README/usage.md that the engine is Linux-only for now (xattr capture via
  `golang.org/x/sys/unix`, `unix.Flock` repository locking, `unix.Lutimes` for symlink mtimes all
  require the `unix` build target); revisit only if macOS/Windows support is explicitly requested.
- Record that `replicate`/`scrub` are intentionally one-shot, cron/systemd-timer-friendly commands,
  not in-process schedulers/daemons, matching the confirmed single-user deployment model.

## Relevant files (primary, for when implementation starts)
- New: `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `LICENSE`,
  `cmd/backup-engine/version.go`, `pkg/pipeline/recover_remote.go`, `pkg/pipeline/scrub.go`,
  `pkg/pack/tailindex.go` (or similar, exported no-decrypt tail-index reader), a chaos-test helper
  (new file under `pkg/pipeline` test-only or a new `internal`/`pkg/chaostest` package).
- Modified: `pkg/storage/minio/minio.go` (+Object Lock), `pkg/pipeline/storage_factory.go`
  (+`ObjectLockRetentionDays`), `cmd/backup-engine/storage.go` (+passphrase/salt file/env flags,
  +object-lock flags), `pkg/chunker/chunker.go` (alloc reduction, post-benchmark),
  `pkg/pipeline/engine.go` (bloom filter hook in `backupOneFile`), README.md/usage.md (security
  section, Linux-only note, `recover`/`scrub`/`version` command docs), project_roadmap.md (mark
  items as they land).

## Verification
- Full gate after every phase: `gofmt -l .`, `go vet ./...`, `go build ./...`, `go test ./...`,
  `go test -race ./pkg/pipeline ./pkg/tui ./pkg/index ./pkg/replicate ./pkg/retention
  ./pkg/manifest` (established convention this whole session).
- Phase 1: manual/CI end-to-end DR drill (init -> backup -> replicate -> wipe local -> recover ->
  restore -> diff) must reproduce the original file tree exactly.
- Phase 2: `govulncheck ./...` clean; fuzz corpus runs for at least 30s locally with zero crashes
  before merging.
- Phase 3: manual test against a real MinIO container with Object Lock enabled at bucket creation
  (`mc mb --with-lock`), confirming `PutPack` sets retention and premature `DeletePack` is rejected
  by the bucket during the retention window.
- Phase 4: benchmark numbers recorded before/after each optimization; no correctness regression in
  the full test suite.

## Decisions
- Linux-only for v1; no cross-platform work now.
- No in-process daemon/scheduler; `replicate`/`scrub` stay one-shot, cron-friendly commands.
- CRC32 pack trailer explicitly deprioritized (BLAKE3 already covers the need) unless the user asks
  for it specifically.
- Passphrase rotation is a known, documented limitation (not fixed in this plan) given it requires
  a full repository re-encryption under the current key-hierarchy design; revisit only as a
  deliberate, separately-scoped architectural change if ever needed.

## Further Considerations
1. **License choice** (blocks Phase 0 step 4): MIT (permissive, simplest) / Apache-2.0 (permissive
   + patent grant) / something else? Recommend MIT for a personal/small-team tool unless there's a
   specific reason for Apache-2.0.
2. **Key-hierarchy redesign for cheap passphrase rotation**: introduce a random per-repository
   "repository master key" that the passphrase-derived Argon2id key only *wraps* (encrypts once,
   stored in repo metadata), so rotating the passphrase becomes "re-wrap one small key" instead of
   "re-encrypt everything." This is a meaningful architectural change with backward-compatibility
   implications for existing repositories - flagging it now as optional future work, not included
   in this plan's scope unless explicitly requested.
3. **`-rebuild-dedup-index` cost**: decrypting every chunk to rebuild `cids`/`cid_by_sid` after a
   disaster-recovery event is O(total repository size) and could be slow for large repositories.
   Acceptable as an opt-in, run-once-after-DR operation, or should it be deferred entirely in favor
   of "just let dedup rebuild itself gradually as new backups happen" (simpler, at the cost of some
   temporary storage bloat)? Recommend making it opt-in as currently planned.
