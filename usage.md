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

## 4. Command reference

### init
- Required: `-repo`, `-passphrase`, `-salt`
- Optional: `-bind-existing` for existing repositories without key-check metadata

### tui
- Starts the full-screen terminal interface:

```bash
./backup-engine tui
```

Running `./backup-engine` without a subcommand also opens the TUI when stdin and stdout are interactive terminals. Non-interactive invocations continue to print command help instead of waiting for input.

### backup
- Required: `-repo`, `-source`, `-passphrase`, `-salt`
- Optional: `-tags` (comma-separated, default `DAILY`)

### list-snapshots
- Required: `-repo`, `-passphrase`, `-salt`
- Optional: `-validate` default `true`

### restore
- Required: `-repo`, `-snapshot`, `-dest`, `-passphrase`, `-salt`

### verify
- Required: `-repo`, `-passphrase`, `-salt`

### doctor
- Required: `-repo`, `-passphrase`, `-salt`

### gc
- Required: `-repo`, `-passphrase`, `-salt`
- Optional:
  - `-keep-daily` default `7`
  - `-keep-weekly` default `4`
  - `-keep-monthly` default `12`
  - `-keep-yearly` default `3`
  - `-grace` default `24h`

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
2. `Repository`: repo path, masked passphrase/salt entry, and key-check validation.
3. `Backup`: source path and retention tags.
4. `Restore`: snapshot ID and destination path.
5. `Snapshots`: readable-status catalog with filtering and direct restore handoff.
6. `Health`: verify stored data and run doctor consistency checks.
7. `Retention`: configure and run GFS garbage collection.
8. `Setup`: initialize key-check metadata or bind an existing repository.

Key controls:
- `up`/`down` or `k`/`j`: navigate.
- `enter`: open a section, accept a candidate, or submit a form.
- `tab`: complete paths and cycle matching directory names; use `up`/`down` to move between fields.
- `/`: filter the snapshot catalog by ID or status.
- `s`: cycle the `strict`, `standard`, and `fast` safety profiles.
- `?`: expand or collapse key help.
- `esc`: return from a form or catalog.
- `q` or `ctrl+c`: quit from dashboard views.

Path completion expands `~`, matches both prefixes and fuzzy character sequences, and marks directories with a trailing path separator. Passphrase and salt fields are masked.

Safety profiles control destructive-action friction:
- `strict`: type `GC` before garbage collection and `RESTORE` before restoring into an existing destination.
- `standard`: type `yes` for those operations.
- `fast`: skips the GC confirmation, but still confirms restore into an existing destination.

## 7. Current limitations

- Snapshot envelope currently uses encrypted JSON metadata; protobuf migration is planned.
- Current implementation focuses on local repository operations first.
- Cloud immutability policy automation (Object Lock lifecycle workflow) is not yet fully wired.
- Restore is file-content focused; advanced metadata/xattr restoration is limited.
- Large-repository performance tuning and extensive chaos scenarios are still evolving.

## 8. Recommended workflow for now

1. Initialize repository: `init`.
2. Run backups on schedule.
3. Validate with `list-snapshots` and `verify`.
4. Use `doctor` periodically for consistency checks.
5. Apply retention with `gc` in maintenance windows.

## 9. Next usability improvements to consider

1. Config file support (`backup-engine.yaml`) for repo defaults.
2. Passphrase file or environment variable support (`-passphrase-file`, `BACKUP_ENGINE_PASSPHRASE`).
3. Persisted TUI theme and safety-profile preferences.
3. GC dry-run mode to preview deletions before mutation.
