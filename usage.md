# Backup Engine Usage Guide

This guide explains how to build and use the tool, what it currently supports, and where the practical limits are today.

## 1. What this tool does

The backup engine is a local-first, deduplicated, encrypted backup system.

Current behavior:
- Chunks files using content-defined chunking (FastCDC).
- Compresses chunks (zstd) before encryption.
- Encrypts chunk payloads and metadata.
- Packs encrypted chunks into packfiles.
- Maintains a local index for deduplication and restore lookups.
- Stores encrypted snapshot metadata.
- Enforces repository key-check metadata to catch passphrase/salt mismatches early.
- Supports restore, verification, doctor checks, and retention-based garbage collection.
- Includes an interactive TUI for guided operations.
- Reports operation phases, aggregate progress, completion metrics, and recovery guidance.
- **Credential input via file or environment variable** (`-passphrase-file`, `-salt-file`, `BACKUP_ENGINE_PASSPHRASE`, `BACKUP_ENGINE_SALT`)
- **S3 Object Lock / WORM retention** (`-s3-object-lock-days`, `-s3-object-lock-compliance`)
- **Disaster recovery** (`recover` rebuilds a repository from a remote replica)
- **Bit-rot scrubbing** (`scrub` validates every packfile's checksum without decrypting)
- **Version introspection** (`version` subcommand)

## 2. Build

From repository root:

```bash
go build ./cmd/backup-engine
```

This produces a binary named `backup-engine` in the current directory.

Optional install to GOPATH/bin:

```bash
go install ./cmd/backup-engine
```

## 3. Quick start

Choose one passphrase and one salt and keep them consistent per repository.
- Passphrase: secret text you control.
- Salt: at least 16 bytes (example: `0123456789abcdef`).

### 3.0 Initialize repository key-check metadata (recommended first step)

```bash
./backup-engine init \
  -repo /tmp/backup-repo \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef"
```

If the repository already contains backup data created before key-check metadata existed, use:

```bash
./backup-engine init \
  -repo /tmp/backup-repo \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef" \
  -bind-existing
```

**Credential input options (all commands):**
- `-passphrase-file <path>` — read passphrase from a file (trimmed of whitespace)
- `-salt-file <path>` — read salt from a file (trimmed of whitespace)
- `BACKUP_ENGINE_PASSPHRASE` environment variable
- `BACKUP_ENGINE_SALT` environment variable

Precedence: flag > file > environment variable. Prefer file or env var; plain flags are visible in shell history and `ps`/`/proc/<pid>/cmdline`.

### 3.1 Create a backup snapshot

```bash
./backup-engine backup \
  -repo /tmp/backup-repo \
  -source /path/to/data \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef" \
  -tags DAILY
```

Output includes the snapshot ID.

### 3.2 List snapshots

```bash
./backup-engine list-snapshots \
  -repo /tmp/backup-repo \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef"
```

This command validates each snapshot by default and prints statuses such as:
- `ok`
- `auth-failed`
- `decode-error`

### 3.3 Restore a snapshot

```bash
./backup-engine restore \
  -repo /tmp/backup-repo \
  -snapshot <SNAPSHOT_HEX_ID> \
  -dest /tmp/restore-output \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef"
```

### 3.4 Verify repository data integrity

```bash
./backup-engine verify \
  -repo /tmp/backup-repo \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef"
```

### 3.5 Run doctor checks (index + data consistency)

```bash
./backup-engine doctor \
  -repo /tmp/backup-repo \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef"
```

### 3.5 Run scrub (bit-rot detection without decrypting)

```bash
./backup-engine scrub \
  -repo /tmp/backup-repo \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef"
```

### 3.6 Run garbage collection with GFS policy

```bash
./backup-engine gc \
  -repo /tmp/backup-repo \
  -passphrase "your-passphrase" \
  -salt "0123456789abcdef" \
  -keep-daily 7 \
  -keep-weekly 4 \
  -keep-monthly 12 \
  -keep-yearly 3 \
  -grace 24h
```

### 3.7 Print version

```bash
./backup-engine version
```

## 4. Command reference

Operational commands accept these output controls:
- `-verbose`: show phase progress and a detailed final summary.
- `-quiet`: show only the primary result.
- `-json`: emit one machine-readable result and suppress progress output.

Interactive terminals show detailed summaries by default. Redirected output remains concise unless `-verbose` is supplied. Progress and advisory messages are written to stderr; primary results are written to stdout.

Every command that opens a repository (`init`, `backup`, `restore`, `list-snapshots`, `snapshot`, `gc`, `verify`, `doctor`, `scrub`) also accepts storage backend flags:
- `-storage-backend`: `local` (default) or `minio` for an S3-compatible bucket.
- `-s3-endpoint`, `-s3-bucket`, `-s3-prefix`: MinIO/S3 connection target (endpoint and bucket are required when `-storage-backend minio`).
- `-s3-access-key`, `-s3-secret-key`: credentials; fall back to `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` when omitted.
- `-s3-use-ssl`: defaults to `true`.
- `-s3-object-lock-days`: if > 0, request S3 Object Lock retention (in days) on every uploaded pack; the bucket must already have Object Lock enabled at creation time.
- `-s3-object-lock-compliance`: use irreversible COMPLIANCE mode instead of GOVERNANCE mode for Object Lock retention.

The bbolt index (chunk mappings, snapshot envelopes, key-check metadata, lifecycle) always stays local under `<repo>/index`; only packfiles move to the configured backend. Use the same storage flags on every command against a given repository.

### init
- Required: `-repo`, `-passphrase`, `-salt` (or `-passphrase-file`/`-salt-file` or env vars)
- Optional: `-bind-existing` for existing repositories without key-check metadata
- Optional: `-s3-object-lock-days`, `-s3-object-lock-compliance` for S3 Object Lock retention

### tui
- Starts the full-screen terminal interface:

```bash
./backup-engine tui
```

Running `./backup-engine` without a subcommand also opens the TUI when stdin and stdout are interactive terminals. Non-interactive invocations continue to print command help instead of waiting for input.

### backup
- Required: `-repo`, `-source`, `-passphrase`, `-salt` (or file/env alternatives)
- Optional: `-tags` (comma-separated, default `DAILY`)

### list-snapshots
- Required: `-repo`, `-passphrase`, `-salt` (or file/env alternatives)
- Optional: `-validate` default `true`

### snapshot

Snapshot lifecycle actions use `backup-engine snapshot <action>`:
- `list`: newest-first catalog with local date/time, status, state, files, and size; add `-include-trash` for recoverable trash.
- `show`: exact local and UTC dates plus lifecycle metadata; requires `-id`.
- `trash`: move to recoverable trash; optional `-trash-for` defaults to `168h` (seven days).
- `untrash`: return a snapshot to active state.
- `pin` / `unpin`: protect from or return to normal GFS expiration.
- `remove`: permanently and instantly delete a snapshot, bypassing trash, and reclaim its unreferenced chunks and packfile space immediately; requires `-yes` to confirm since it cannot be undone.
- `edit`: update encrypted `-labels`, `-note`, and optional RFC3339 `-retain-until`; omitted values remain unchanged and `-clear-retain-until` removes the deadline.

Trash is reversible until its purge date and does not immediately reclaim space. Garbage collection reclaims unshared chunks only after the recovery window expires. `remove` skips this entirely: it is irreversible but frees destination space right away.

### restore
- Required: `-repo`, `-snapshot`, `-dest`, `-passphrase`, `-salt` (or file/env alternatives)

### verify
- Required: `-repo`, `-passphrase`, `-salt` (or file/env alternatives)

### doctor
- Required: `-repo`, `-passphrase`, `-salt` (or file/env alternatives)

### scrub
- Required: `-repo`, `-passphrase`, `-salt` (or file/env alternatives)
- Re-validates every packfile's BLAKE3 trailer checksum without decrypting anything, catching bit-rot on data at rest (including packs no live snapshot currently references).

### gc
- Required: `-repo`, `-passphrase`, `-salt` (or file/env alternatives)
- Optional:
  - `-keep-daily` default `7`
  - `-keep-weekly` default `4`
  - `-keep-monthly` default `12`
  - `-keep-yearly` default `3`
  - `-grace` default `24h`

### version
- Prints the build version (set via `-ldflags -X main.version=...` at build time)

### replicate
- `backup-engine replicate run` copies packfiles, encrypted snapshot manifests, and the CID directory from the repository's own storage backend to a second, offsite backend. Idempotent: re-running skips items already present at the remote.
- Required: `-repo`, `-passphrase`, `-salt` (or file/env alternatives), `-remote-storage-backend minio`, `-remote-s3-endpoint`, `-remote-s3-bucket`
- Optional: `-remote-s3-prefix`, `-remote-s3-access-key`/`-remote-s3-secret-key` (fall back to `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`), `-remote-s3-use-ssl`, `-remote-s3-object-lock-days`, `-remote-s3-object-lock-compliance`
- The remote backend must not be `local`. Packs, manifests, and the CID directory are replicated under separate `packs`/`manifests`/`index` sub-prefixes so they cannot collide.

### recover
- `backup-engine recover` rebuilds a fresh, empty repository directory entirely from a remote populated by a prior `replicate run`: encrypted snapshot manifests, the chunk-location index (parsed directly from each remote pack's own trailer, no decryption needed), and the pack contents themselves.
- Required: `-repo` (must be empty/uninitialized), `-passphrase`, `-salt` (or file/env alternatives), `-remote-storage-backend minio`, `-remote-s3-endpoint`, `-remote-s3-bucket`
- Optional: `-remote-s3-prefix`, `-remote-s3-access-key`/`-remote-s3-secret-key`, `-remote-s3-use-ssl`
- **Important:** a chunk's decryption key is derived from its CID, and StorageID cannot be turned back into a CID without decrypting the chunk — a circular, impossible requirement. `recover` can only make data readable again if the remote also has the encrypted CID directory pushed by `replicate run`. A repository replicated with an older build needs one more `replicate run` after upgrading; otherwise `recover` will rebuild the snapshot/chunk-location structure but every chunk will remain permanently undecryptable. `recover` reports whether the CID directory was found and runs a full `verify` at the end so this is never silent.

### history
- `backup-engine history list` prints persisted, encrypted transaction history entries (newest first) for every operation type, including status, duration, and (on failure) the error message. `-limit` bounds how many entries are returned (default 50, `0` for all).
- `backup-engine history clear -before <RFC3339>` permanently deletes entries completed before the given timestamp.
- Required: `-repo`, `-passphrase`, `-salt` (or file/env alternatives)

### replicate
- `backup-engine replicate run` copies packfiles and encrypted snapshot manifests from the
  repository's own storage backend to a second, offsite backend. Idempotent: re-running skips
  items already present at the remote.
- Required: `-repo`, `-passphrase`, `-salt`, `-remote-storage-backend minio`, `-remote-s3-endpoint`, `-remote-s3-bucket`
- Optional: `-remote-s3-prefix`, `-remote-s3-access-key`/`-remote-s3-secret-key` (fall back to `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`), `-remote-s3-use-ssl`
- The remote backend must not be `local`. Packs and manifests are replicated under separate `packs`/`manifests` sub-prefixes so they cannot collide.

### history
- `backup-engine history list` prints persisted, encrypted transaction history entries (newest
  first) for every operation type, including status, duration, and (on failure) the error message.
  `-limit` bounds how many entries are returned (default 50, `0` for all).
- `backup-engine history clear -before <RFC3339>` permanently deletes entries completed before the
  given timestamp.
- Required: `-repo`, `-passphrase`, `-salt`

## 5. Operational guidelines

- Keep passphrase and salt safe and consistent for each repository.
- Run `init` once per repository before regular operations.
- Treat passphrase loss as data loss risk.
- Run `verify` regularly (for example weekly).
- Run `doctor` before and after large migrations.
- Run `gc` in maintenance windows for large repositories.
- Keep independent copies of repository data (local and offsite).

## 6. Terminal Interface

The TUI is organized around a persistent dashboard with navigation on the left and the active workflow on the right.

Main sections:
1. `Overview`: repository state and next action.
2. `Activity`: bounded phase history for the current TUI session.
3. `Repository`: repo path, masked passphrase/salt entry, and key-check validation.
4. `Backup`: source path, retention tags, and planned transaction summary.
5. `Restore`: snapshot ID, destination path, and overwrite warning.
6. `Snapshots`: readable-status catalog with filtering and direct restore handoff.
7. `History`: persistent, encrypted transaction history across sessions (backup/restore/gc/verify/doctor/replicate/remove/init).
8. `Health`: verify stored data and run doctor consistency checks.
9. `Scrub`: re-validate every packfile's checksum for bit-rot without decrypting.
10. `Retention`: review GFS policy and run garbage collection.
11. `Replicate`: copy packs, manifests, and the CID directory to an offsite backend.
12. `Recover`: rebuild this repository path entirely from an offsite replica after total local loss.
13. `Setup`: initialize key-check metadata or bind an existing repository.

Key controls:
- `up`/`down` or `k`/`j`: navigate.
- `enter`: open a section, accept a candidate, or submit a form.
- `tab`: complete paths and cycle matching directory names; use `up`/`down` to move between fields.
- `/`: filter the snapshot catalog by ID or status.
- `s`: open descriptions and select the `strict`, `standard`, or `fast` safety profile.
- `d`: trash or untrash the selected snapshot.
- `p`: pin or unpin the selected snapshot.
- `x`: permanently and instantly remove the selected snapshot (bypasses trash); requires typed `REMOVE` confirmation unless the Fast safety profile is active.
- `e`: edit encrypted labels, note, and retain-until date.
- `?`: expand or collapse key help.
- `esc`: return from a form or catalog.
- `q` or `ctrl+c`: quit from dashboard views.

Path completion expands `~`, matches both prefixes and fuzzy character sequences, and marks directories with a trailing path separator. Passphrase and salt fields are masked.

Every input includes inline guidance explaining what the value controls, its accepted format, and relevant safety implications. Read the description before submitting retention values, repository credentials, restore destinations, or existing-data binding choices.

Long-running operations display:
- the current phase and elapsed time;
- aggregate file, snapshot, or chunk counters when totals are known;
- a bounded activity log with timestamps;
- a completion summary with measured results and a recommended next step.

Normal activity excludes passphrases, salts, encryption keys, and per-file names. Activity is not persisted after the TUI exits.

Safety profiles control destructive-action friction:
- `strict`: typed confirmation for overwrite, trash, retention changes, and garbage collection.
- `standard`: type `yes` for those operations.
- `fast`: skips confirmation only for reversible actions; garbage collection still requires typing `GC`.

Safety profiles reduce accidental input. They are not backups, encryption, permissions, or immutability controls.

## 7. Cancellation and recovery

Backup mutations are protected by a repository-wide one-writer lock. New packs and mappings are recorded in a durable operation journal. If a backup is cancelled before snapshot commit, operation-owned mappings and packs are removed. If the process stops unexpectedly, reopening the repository replays cleanup from the journal. Snapshot insertion and journal removal occur in one bbolt transaction, so recovery sees either an incomplete operation to remove or a complete snapshot to preserve.

Do not use rsync against a live repository. It cannot create a consistent checkpoint across the bbolt index and packfiles. Rsync is intentionally not integrated; future replication will use the native storage abstraction and verified repository checkpoints.

## 8. Current limitations

- Linux-only for now: xattr capture, repository locking, and symlink mtime preservation all use `golang.org/x/sys/unix`. macOS/Windows support is not planned unless separately requested.
- `replicate`/`scrub` are one-shot commands meant to be invoked manually or via cron/systemd timers, not always-on daemons; there is no in-process scheduler.
- There is no passphrase/salt rotation. Because the encryption key hierarchy is entirely passphrase-derived, rotating either would require re-encrypting the whole repository; this is not currently supported.
- S3 Object Lock retention is opt-in per replicate/init call (`-s3-object-lock-days`); it is not automatically derived from a repository's GFS retention policy, and bucket lifecycle policy automation is not implemented.
- Backup, GC, initialization, and lifecycle mutations use a one-writer repository lock.

## 9. Recommended workflow for now

1. Initialize repository: `init` (optionally with `-s3-object-lock-days` for WORM retention).
2. Run backups on schedule (use `-passphrase-file`/`-salt-file` or env vars for credentials).
3. Validate with `list-snapshots` and `verify`.
4. Use `doctor` periodically for consistency checks.
5. Run `scrub` periodically to detect bit-rot on data at rest.
6. Apply retention with `gc` in maintenance windows.
7. Run `replicate run` on schedule to maintain an offsite copy with the CID directory for disaster recovery.

## 10. Next usability improvements to consider

1. Config file support (`backup-engine.yaml`) for repo defaults.
2. Passphrase rotation support (requires key-hierarchy redesign).
3. Automatic Object Lock retention derived from GFS policy.
3. Persisted TUI theme and safety-profile preferences.
3. GC dry-run mode to preview deletions before mutation.
