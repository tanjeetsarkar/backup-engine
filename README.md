# Backup Engine

Backup Engine is an experimental, local-first backup tool written in Go. It creates encrypted, content-defined, deduplicated snapshots and provides both a full-screen terminal interface and scriptable CLI commands.

> [!IMPORTANT]
> This project is under active development. The local backup, restore, verification, diagnostics, and retention paths are implemented and tested, but production hardening, protobuf manifests, cloud replication, and immutable remote retention are not complete.

## Features

- FastCDC content-defined chunking
- zstd compression before encryption
- XChaCha20-Poly1305 authenticated encryption
- BLAKE3 content and storage identifiers
- Deduplication across snapshots in one repository
- Encrypted snapshot metadata (protobuf wire format) and repository key-check validation
- Symlinks, POSIX permissions, ownership, and extended attributes (including POSIX ACLs, carried as xattrs) are captured and restored
- Local packfile storage with a bbolt index
- Snapshot restore, integrity verification, and repository diagnostics
- Grandfather-Father-Son retention and garbage collection
- Bubble Tea terminal interface with path completion and safety profiles
- Phase progress, operation activity, completion summaries, and recovery guidance
- Concise, verbose, quiet, and JSON CLI output modes

## Quick Start

Requirements: Go 1.27 or a compatible newer toolchain.

```bash
go build ./cmd/backup-engine
```

Initialize a repository. The salt must contain at least 16 bytes. Keep both the passphrase and salt available: losing either can make the repository unrecoverable.

```bash
./backup-engine init \
  -repo /tmp/backup-repo \
  -passphrase "choose-a-strong-passphrase" \
  -salt "0123456789abcdef"
```

Create a snapshot:

```bash
./backup-engine backup \
  -repo /tmp/backup-repo \
  -source ~/Documents \
  -passphrase "choose-a-strong-passphrase" \
  -salt "0123456789abcdef" \
  -tags DAILY
```

List snapshots and restore one:

```bash
./backup-engine list-snapshots \
  -repo /tmp/backup-repo \
  -passphrase "choose-a-strong-passphrase" \
  -salt "0123456789abcdef"

./backup-engine restore \
  -repo /tmp/backup-repo \
  -snapshot <SNAPSHOT_ID> \
  -dest /tmp/restored \
  -passphrase "choose-a-strong-passphrase" \
  -salt "0123456789abcdef"
```

Run verification after important backups and restores:

```bash
./backup-engine verify \
  -repo /tmp/backup-repo \
  -passphrase "choose-a-strong-passphrase" \
  -salt "0123456789abcdef"
```

See [usage.md](usage.md) for the complete command reference and operational workflow.

## Terminal Interface

Run the binary without arguments in an interactive terminal, or use the explicit subcommand:

```bash
./backup-engine
# or
./backup-engine tui
```

The dashboard contains Overview, Activity, Repository, Backup, Restore, Snapshots, History, Health, Retention, and Setup sections. Operations show their current phase, aggregate counters, elapsed time, recent activity, and a final transaction summary with a suggested next step.

Key controls:

| Key | Action |
| --- | --- |
| `up` / `down`, `k` / `j` | Navigate sections and lists |
| `enter` | Open, select, or submit |
| `tab` | Complete paths or cycle matching directories |
| `/` | Filter snapshots |
| `s` | Open descriptions and select a safety profile |
| `d` | Trash or untrash the selected snapshot |
| `p` | Pin or unpin the selected snapshot |
| `x` | Permanently and instantly remove the selected snapshot (bypasses trash) |
| `e` | Edit encrypted labels, note, and retain-until date |
| `?` | Toggle expanded help |
| `esc` | Return from a form or result |
| `q`, `ctrl+c` | Quit from dashboard views |

Passphrases and salts are masked. Activity history is bounded and exists only for the current TUI session. Every operation (backup, restore, gc, verify, doctor, snapshot remove, replicate, init) is additionally recorded as an encrypted entry in a persistent transaction history that survives across sessions and process restarts; see the `history` command and the TUI's History section below.

Snapshot rows show the creation date in local time with timezone, status, lifecycle state, file count, and logical size. The selected details show the exact UTC timestamp, full metadata, labels, note, and retention overrides.

Safety profiles are interaction safeguards, not substitutes for independent backups or immutable storage. Strict explains and strongly confirms destructive actions; Standard confirms destructive actions with less friction; Fast skips confirmation only for reversible actions. Garbage collection always requires explicit confirmation.

## Managing Snapshots

Manual removal uses recoverable trash rather than immediate deletion. A trashed snapshot is hidden from the active list but remains restorable until its purge date, seven days by default. Storage is reclaimed only when garbage collection runs after that deadline.

For cases where destination space must be reclaimed right away, `snapshot remove` (CLI) or `x` (TUI) permanently deletes a snapshot immediately, bypassing trash entirely. It removes the snapshot's now-unreferenced chunks and repacks any affected packfiles on the spot, so disk usage drops immediately rather than waiting for a scheduled `gc`. This is irreversible and requires explicit confirmation (`-yes` on the CLI, a typed `REMOVE` in the TUI unless the Fast safety profile is active).

```bash
./backup-engine snapshot list -repo /tmp/backup-repo -passphrase "..." -salt "..."
./backup-engine snapshot show -repo /tmp/backup-repo -passphrase "..." -salt "..." -id <ID>
./backup-engine snapshot trash -repo /tmp/backup-repo -passphrase "..." -salt "..." -id <ID>
./backup-engine snapshot untrash -repo /tmp/backup-repo -passphrase "..." -salt "..." -id <ID>
./backup-engine snapshot pin -repo /tmp/backup-repo -passphrase "..." -salt "..." -id <ID>
./backup-engine snapshot remove -repo /tmp/backup-repo -passphrase "..." -salt "..." -id <ID> -yes
./backup-engine snapshot edit -repo /tmp/backup-repo -passphrase "..." -salt "..." -id <ID> -labels important -note "Monthly archive" -retain-until 2030-01-01T00:00:00Z
```

Pinned snapshots and active snapshots with a future retain-until deadline override normal GFS expiration. Explicit trash takes precedence after its recovery window expires. Labels, notes, pins, and lifecycle dates are encrypted and do not alter immutable snapshot contents or IDs.

## CLI Output

Operational commands support consistent output controls:

- `--verbose`: show phase progress and detailed summaries.
- `--quiet`: retain only the primary command result.
- `--json`: emit one machine-readable final result and disable progress output.

Interactive terminals receive richer summaries by default. Redirected output remains concise so existing shell workflows continue to work.

## How It Works

```mermaid
flowchart LR
    A[Files] --> B[FastCDC chunks]
    B --> C[BLAKE3 content IDs]
    C --> D{Already indexed?}
    D -->|Yes| E[Reuse stored chunk]
    D -->|No| F[zstd compression]
    F --> G[XChaCha20-Poly1305 encryption]
    G --> H[Encrypted packfiles]
    H --> I[bbolt index]
    E --> J[Encrypted snapshot manifest]
    I --> J
    J --> K[Restore / Verify / Retention]
```

Backup data is chunked by content, compressed, encrypted, and aggregated into immutable packfiles. The local index maps content identifiers to encrypted storage locations. Snapshot manifests reference those locations and are encrypted separately. Restore resolves each referenced chunk, authenticates and decrypts it, decompresses it, and verifies the reconstructed file hash.

For format and security details, see [architecture_specification.md](architecture_specification.md).

## Repository Layout

A local repository currently contains:

```text
<repository>/
├── index/
│   └── index.db
└── data/
    └── packs/
        └── <blake3-pack-id>.pack
```

The bbolt database contains chunk mappings, snapshot envelopes, and key-check metadata. Packfiles contain encrypted chunk records and a checksummed tail index.

Do not edit repository files manually. Keep independent copies of the entire repository directory.

## Storage Backends

By default, every command that opens a repository uses local filesystem packfile storage under `<repository>/data`. The bbolt index (chunk mappings, snapshot envelopes, key-check metadata) is always local.

Packfiles can instead be stored in a MinIO or other S3-compatible bucket by adding `-storage-backend minio` plus connection flags to any repository command (`init`, `backup`, `restore`, `list-snapshots`, `snapshot`, `gc`, `verify`, `doctor`):

```bash
./backup-engine backup \
  -repo /tmp/backup-repo \
  -source ~/Documents \
  -passphrase "choose-a-strong-passphrase" \
  -salt "0123456789abcdef" \
  -storage-backend minio \
  -s3-endpoint localhost:9000 \
  -s3-bucket backup-engine-packs \
  -s3-prefix optional/key/prefix \
  -s3-access-key "$AWS_ACCESS_KEY_ID" \
  -s3-secret-key "$AWS_SECRET_ACCESS_KEY" \
  -s3-use-ssl=true
```

`-s3-access-key`/`-s3-secret-key` fall back to the `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` environment variables when omitted. Use the same `-storage-backend`/`-s3-*` flags consistently across every command against a given repository; mixing backends for the same repository will make packs written under one backend invisible to commands using the other.

## Replication

`replicate run` copies a repository's packfiles and encrypted snapshot manifests to a second, offsite storage backend, without touching the primary repository's own backend. It is idempotent: already-replicated items are skipped, so it is safe to run repeatedly (e.g. on a schedule). Packs and manifests are stored under separate `packs`/`manifests` sub-prefixes at the remote so they can never collide.

```bash
./backup-engine replicate run \
  -repo /tmp/backup-repo \
  -passphrase "choose-a-strong-passphrase" \
  -salt "0123456789abcdef" \
  -remote-storage-backend minio \
  -remote-s3-endpoint offsite-host:9000 \
  -remote-s3-bucket backup-engine-offsite \
  -remote-s3-access-key "$OFFSITE_ACCESS_KEY" \
  -remote-s3-secret-key "$OFFSITE_SECRET_KEY"
```

Use `-remote-*` flags (mirroring the `-storage-backend`/`-s3-*` flags above) to describe the replication target; the repository's own `-storage-backend`/`-s3-*` flags (if any) describe where it is currently reading from. The remote backend must not be `local`. Replication never deletes data at either end, and does not currently throttle bandwidth or set retention/immutability policy on the remote bucket.

## Commands

| Command | Purpose |
| --- | --- |
| `init` | Initialize repository directories and key-check metadata |
| `tui` | Open the terminal interface |
| `backup` | Create an encrypted snapshot |
| `restore` | Restore a snapshot into a destination |
| `list-snapshots` | List snapshots and metadata readability status |
| `snapshot` | List, inspect, trash, restore, pin, remove, or edit snapshot lifecycle metadata |
| `verify` | Authenticate and reconstruct all referenced data |
| `doctor` | Check index mappings, pack records, and snapshot data |
| `gc` | Apply GFS retention and remove unreferenced chunks |
| `replicate` | Copy packfiles and manifests to a second, offsite storage backend |
| `history` | List or prune the persistent, encrypted transaction history |

## Troubleshooting

| Message or status | Meaning | Next step |
| --- | --- | --- |
| `repository key-check failed` | Passphrase or salt does not match | Reconnect with the credentials originally used for the repository |
| `repository contains data but no key-check metadata` | Older repository needs explicit binding | Run `init -bind-existing` only after confirming credentials |
| `auth-failed` | Snapshot metadata cannot be authenticated | Verify credentials; if correct, run `doctor` and restore damaged data from another copy |
| `decode-error` | Snapshot metadata is malformed or corrupted | Run `doctor`; avoid GC until the repository is understood |
| `snapshot not found` | The supplied ID is absent | Refresh `list-snapshots` and select an existing ID |
| Permission denied | A source, destination, or repository path is inaccessible | Check ownership and read/write permissions |
| Hash or storage-ID mismatch | Stored data or index mappings are inconsistent | Stop destructive maintenance, run `doctor`, and recover from another copy |

## Current Limitations

- Remote object-lock and lifecycle policy automation are not complete.
- Backup, GC, initialization, and lifecycle mutations use a one-writer repository lock.
- Graceful pre-commit backup cancellation rolls back new mappings and packs. A durable journal cleans interrupted pre-commit backups when the repository reopens; a snapshot committed atomically before cancellation remains a valid snapshot.
- GC repack now uses a durability journal analogous to the backup journal: an interrupted repack is rolled back to its pre-repack state on reopen, and the next GC run retries it.
- Large-scale performance and interruption testing remain ongoing.

## Why Not Rsync?

Rsync is not integrated. Its file-delta model does not understand the atomic relationship among encrypted manifests, bbolt mappings, and immutable packfiles, and it is not a native object-store transport. Copying a live repository with rsync can capture mismatched index and pack state. The engine instead uses content-defined deduplication, checksummed packs, temporary files, atomic rename, one-writer locking, rollback, and recovery journals. Future replication should operate on a consistent repository checkpoint through the storage abstraction.

## Development

```bash
gofmt -w ./cmd ./pkg
go test ./...
go vet ./...
go build ./cmd/backup-engine
```

Project direction and acceptance criteria are tracked in [project_roadmap.md](project_roadmap.md). Detailed usage examples are in [usage.md](usage.md).

## Roadmap

Planned work includes protobuf manifest evolution, cloud replication with retry and consistency checks, immutable remote retention controls, persistent operation history, broader chaos testing, and performance characterization. These are roadmap items, not current guarantees.
