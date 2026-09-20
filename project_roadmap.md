# Implementation Roadmap: Next-Gen Zero-Knowledge Deduplicated Backup Engine

This roadmap establishes a six-phase engineering path for building, stabilizing, and deploying a production-grade, zero-knowledge, chunk-level deduplicated 3-2-1 backup system in Go.

---

## Phase 1: Cryptographic & Ingestion Foundations
**Target Duration:** Weeks 1–3  
**Objective:** Deliver an in-memory streaming pipeline capable of processing raw byte streams into authenticated, deduplicated, and compressed blocks.

- [ ] **FastCDC Implementation:**
  - [x] Build normalized Content-Defined Chunking with gear-table hashing (`pkg/chunker/chunker.go`: 256-entry gear table, two-phase normalized scan).
  - [x] Calibrate parameters: Min 256 KiB, Normal/Target 1 MiB, Max 4 MiB (`MinChunkSize`/`TargetChunkSize`/`MaxChunkSize` match exactly, with strict/normal masks for each region).
  - [ ] Implement zero-allocation streaming window over `io.Reader` (current `FastCDC` reuses one internal ring-style buffer across refills but still allocates a new `[]byte` per emitted chunk; not fully zero-allocation).
- [x] **Cryptographic Engine:**
  - [x] Argon2id Master Key derivation with user salt ($T=3, M=64\text{ MiB}, P=4$) (`pkg/crypto/crypto.go` `NewKeyManager`).
  - [x] Single-tenant convergent key derivation via HKDF-Expand and HMAC-SHA256 (`deriveSubkeys`, `DeriveChunkKey`, `DeriveStorageID`).
  - [x] Payload encryption using XChaCha20-Poly1305 with synthetic nonces to guarantee deterministic ciphertext under identical tenant keys (`EncryptChunk`: nonce = `BLAKE3(ChunkKey || Plaintext)[0:24]`).
  - [x] Compute unencrypted BLAKE3 Content IDs (CID) and tenant-isolated `StorageID` identifiers (`ComputeCID`, `DeriveStorageID`).
- [ ] **Compression Integration:**
  - [x] Integrate streaming Zstandard (`pkg/pipeline/engine.go` compressor/decompressor via `klauspost/compress/zstd`).
  - [ ] Benchmark chunk pipeline throughput (target: $>1.5\text{ GB/s}$ per core) — no benchmark exists yet.

---

## Phase 2: Indexing, Storage Abstraction & Packfile Packaging
**Target Duration:** Weeks 4–6  
**Objective:** Prevent small-object amplification on cloud targets by implementing packfile aggregation and high-performance local indexing.

- [ ] **Packfile Engine (`.pack`):**
  - [x] Implement contiguous binary format appending encrypted chunks up to 16–32 MiB (`pkg/pack/pack.go` `PackfileBuilder`, default target 16 MiB).
  - [x] Write trailing index structures storing `[StorageID, Offset, Length, Checksum]` (tail index + trailer).
  - [ ] Add CRC32/BLAKE3 integrity trailers for fast corruption scanning — only a BLAKE3 trailer checksum is implemented; CRC32 was not added.
- [ ] **Local Embedded Cache (LSM / B+Tree):**
  - [x] Integrate `bbolt` to store local mappings of `CID -> StorageID -> (PackID, Offset)` (`pkg/index/index.go`).
  - [ ] Introduce an in-memory Blocked Bloom Filter to short-circuit index lookups for brand-new chunks — not implemented; new-chunk checks go directly to bbolt.
- [x] **Storage Engine Abstraction:**
  - [x] Define unified `StorageEngine` Go interface (`PutPack`, `GetPack`, `GetChunkRange`, `DeletePack`, `ListPacks`) (`pkg/pack/pack.go`).
  - [x] Implement local filesystem driver (`LocalFilesystemStorage`) and a MinIO/S3-compatible driver, both wired into the main pipeline via `pkg/pipeline.NewStorageEngine` (see Phase 5).

---

## Phase 3: Merkle Snapshot Manifests & Point-in-Time Restoration
**Target Duration:** Weeks 7–9  
**Objective:** Represent point-in-time filesystem state using immutable directed acyclic graphs (DAGs) and achieve fast streaming restores.

- [ ] **Merkle DAG Tree Construction:**
  - [x] Represent files as ordered lists of `StorageID`s with POSIX metadata (permissions, timestamps, ownership, symlinks, xattrs/ACLs).
  - [ ] Represent directories as sorted trees of child nodes (manifests are currently a flat file list per snapshot root; the wire format already supports nested `DirectoryNode`s for when this lands).
  - [x] Compute deterministic Merkle root hashes for files and trees.
- [x] **Encrypted Manifest Generation:**
  - [x] Serialize manifests using a protobuf wire encoding (hand-rolled via `google.golang.org/protobuf/encoding/protowire`, no protoc toolchain required); legacy JSON-encoded snapshots remain readable.
  - [x] Encrypt manifests with the derived Metadata Key ($K_{\text{meta}}$).
  - [x] Implement transactional manifest committing (snapshot insertion and journal removal in one bbolt transaction).
- [x] **Snapshot Lifecycle Management:**
  - Expose local and UTC creation dates, file/byte totals, status, and parent metadata.
  - Add encrypted trash, pin, retain-until, labels, and notes with backward-compatible defaults.
  - Add TUI and CLI snapshot management workflows.
  - Add an instant, trash-bypassing hard delete (`snapshot remove` / TUI `x`) that reclaims unreferenced chunks and repacks affected packs immediately.
- [x] **Persistent Transaction History:**
  - Encrypted `transaction_history` bbolt bucket (chronologically ordered, timestamp+sequence keyed) recording every backup/restore/gc/verify/doctor/replicate/remove/init outcome, surviving across sessions and process restarts (previously only an in-session, in-memory TUI activity log).
  - `backup-engine history list|clear` CLI command and a read-only TUI "History" section.
- [ ] **Streamed Restoration Engine:**
  - [ ] Implement selective path restoration by resolving manifest Merkle paths (current `restore` command always restores an entire snapshot; there is no path-scoped restore flag yet).
  - [x] Implement byte-range packfile retrieval (avoiding full packfile downloads) (`storage.GetChunkRange` used throughout restore/verify/doctor/GC).
  - [x] Verify Poly1305 authentication tags and BLAKE3 hashes on every restored chunk (`DecryptChunk` AEAD verification + post-restore `ComputeCID` content-hash comparison in `materializeFileContentDetailed`).

---

## Phase 4: GFS Retention Policy & Safe Garbage Collection
**Target Duration:** Weeks 10–12  
**Objective:** Automate retention windows and safely prune unreferenced storage blocks without risking data loss from in-flight concurrent backups.

- [x] **GFS Policy Evaluator:**
  - [x] Implement classification engine: Daily (Son), Weekly (Father), Monthly/Yearly (Grandfather) (`pkg/retention/gfs.go` `EvaluateGFS`).
  - [x] Evaluate point-in-time manifests against retention quotas and mark expired manifests.
  - [x] Lifecycle precedence implemented for pins, retain-until overrides, recoverable trash, and expired trash (`pkg/pipeline/engine.go` `RunGCDetailed`).
- [x] **Two-Phase Mark-and-Sweep Garbage Collector:**
  - [x] **Phase 1 (Mark):** Traverse all active, retained manifests; construct a live `StorageID` set (`GarbageCollector.Run`).
  - [x] **Phase 2 (Sweep):** Scan existing packfile index tables and collect candidate unreferenced chunks.
  - [x] **Epoch Safety Guard:** Enforce a minimum grace period (default $T_{\text{grace}} = 24\text{ hours}$, CLI `-grace`) based on chunk upload timestamps to resolve concurrent backup race conditions.
- [x] **Packfile Compaction Routine:**
  - [x] Calculate dead-chunk fragmentation ratio per packfile.
  - [x] Repack living chunks from packs with $>40\%$ dead space into new packs; atomically update index and purge stale packfiles (durability journal added — see Phase 6).

---

## Phase 5: 3-2-1 Cloud Fabric & Immutability Enforcement
**Target Duration:** Weeks 13–15  
**Objective:** Deliver cross-destination replication with cloud immutability to safeguard against ransomware and credential compromise.

- [x] **Cloud Storage Backend:**
  - [x] MinIO/S3-compatible driver (`minio-go/v7`) wired into the main pipeline: `pkg/pipeline.NewStorageEngine` selects between the local filesystem and MinIO backends, injected via `EngineConfig.Storage`/`InitConfig.Storage` and selectable per-command with `-storage-backend`/`-s3-*` CLI flags (index/bbolt metadata stays local; only packfiles move to the configured backend).
  - [ ] Multipart streaming uploads for very large packfiles (current MinIO backend uses single-shot `PutObject`).
- [ ] **Ransomware Protection (WORM):**
  - Implement S3 Object Lock configuration (Compliance Mode).
  - Calculate and set retention headers dynamically based on GFS expiration schedules.
- [x] **3-2-1 Replication Orchestrator:**
  - [x] Primary working storage $\to$ Local staging/cache repository (NVMe/SSD) (existing local backend).
  - [x] One-shot synchronization: local storage $\to$ offsite cloud object store (`pkg/replicate.Daemon`, `Engine.ReplicateDetailed`, `backup-engine replicate run`). Idempotent (skips already-replicated items), retries transient failures with exponential backoff, and supports an optional bandwidth cap.
  - [ ] Background/scheduled daemon mode (current implementation is a one-shot pass invoked on demand, e.g. via cron, not a long-running background process).

---

## Phase 6: System Stabilization, Verification & Chaos Drills
**Target Duration:** Weeks 16–18  
**Objective:** Validate performance envelopes, memory ceilings, corruption detection, and automated disaster recovery.

- [ ] **Scrubbing & Bit-Rot Detection:**
  - Background daemon performing cryptographic verification of remote chunks via byte-range reads.
- [ ] **Failure Mode & Chaos Testing:**
  - Kill backup client mid-chunk/mid-pack upload; verify repository integrity on subsequent runs.
  - Inject bit-flips into remote chunks; confirm client fails safely with AEAD authentication errors.
  - Simulate clock drift and verify GFS retention invariants.
  - [x] Graceful backup cancellation rolls back operation-owned chunk mappings and packs.
  - [x] Durable backup journals recover interrupted pre-commit operations on repository open.
  - [x] Add equivalent durable journaling for interrupted GC repacks.
- [ ] **Disaster Recovery Automation:**
  - Headless CI/CD container restoring the entire snapshot onto fresh storage using only master passphrase and S3 bucket credentials.