# Implementation Roadmap: Next-Gen Zero-Knowledge Deduplicated Backup Engine

This roadmap establishes a six-phase engineering path for building, stabilizing, and deploying a production-grade, zero-knowledge, chunk-level deduplicated 3-2-1 backup system in Go.

---

## Phase 1: Cryptographic & Ingestion Foundations
**Target Duration:** Weeks 1–3  
**Objective:** Deliver an in-memory streaming pipeline capable of processing raw byte streams into authenticated, deduplicated, and compressed blocks.

- [ ] **FastCDC Implementation:**
  - Build normalized Content-Defined Chunking with gear-table hashing.
  - Calibrate parameters: Min 256 KiB, Normal/Target 1 MiB, Max 4 MiB.
  - Implement zero-allocation streaming window over `io.Reader`.
- [ ] **Cryptographic Engine:**
  - Implement Argon2id Master Key derivation with user salt ($T=3, M=64\text{ MiB}, P=4$).
  - Implement single-tenant convergent key derivation via HKDF-Expand and HMAC-SHA256.
  - Implement payload encryption using XChaCha20-Poly1305 with synthetic nonces to guarantee deterministic ciphertext under identical tenant keys.
  - Compute unencrypted BLAKE3 Content IDs (CID) and tenant-isolated `StorageID` identifiers.
- [ ] **Compression Integration:**
  - Integrate streaming Zstandard (level 3 for high-throughput backup mode).
  - Benchmark chunk pipeline throughput (target: $>1.5\text{ GB/s}$ per core).

---

## Phase 2: Indexing, Storage Abstraction & Packfile Packaging
**Target Duration:** Weeks 4–6  
**Objective:** Prevent small-object amplification on cloud targets by implementing packfile aggregation and high-performance local indexing.

- [ ] **Packfile Engine (`.pack`):**
  - Implement contiguous binary format appending encrypted chunks up to 16–32 MiB.
  - Write trailing index structures storing `[StorageID, Offset, Length, Checksum]`.
  - Add CRC32/BLAKE3 integrity trailers for fast corruption scanning.
- [ ] **Local Embedded Cache (LSM / B+Tree):**
  - Integrate `bbolt` or `LMDB` to store local mappings of `CID -> StorageID -> (PackID, Offset)`.
  - Introduce an in-memory Blocked Bloom Filter to short-circuit index lookups for brand-new chunks.
- [ ] **Storage Engine Abstraction:**
  - Define unified `StorageEngine` Go interface (`PutPack`, `GetPack`, `GetChunkRange`, `DeletePack`, `ListPacks`).
  - Implement local filesystem driver (`LocalStorageEngine`).

---

## Phase 3: Merkle Snapshot Manifests & Point-in-Time Restoration
**Target Duration:** Weeks 7–9  
**Objective:** Represent point-in-time filesystem state using immutable directed acyclic graphs (DAGs) and achieve fast streaming restores.

- [ ] **Merkle DAG Tree Construction:**
  - Represent files as ordered lists of `StorageID`s with POSIX metadata (permissions, timestamps, xattrs).
  - Represent directories as sorted trees of child nodes.
  - Compute deterministic Merkle root hashes for files and trees.
- [ ] **Encrypted Manifest Generation:**
  - Serialize manifests using Protocol Buffers or MessagePack.
  - Encrypt manifests with the derived Metadata Key ($K_{\text{meta}}$).
  - Implement transactional manifest committing (staged manifest write $\to$ sync $\to$ atomic pointer swap).
- [ ] **Streamed Restoration Engine:**
  - Implement selective path restoration by resolving manifest Merkle paths.
  - Implement byte-range packfile retrieval (avoiding full packfile downloads).
  - Verify Poly1305 authentication tags and BLAKE3 hashes on every restored chunk.

---

## Phase 4: GFS Retention Policy & Safe Garbage Collection
**Target Duration:** Weeks 10–12  
**Objective:** Automate retention windows and safely prune unreferenced storage blocks without risking data loss from in-flight concurrent backups.

- [ ] **GFS Policy Evaluator:**
  - Implement classification engine: Daily (Son), Weekly (Father), Monthly/Yearly (Grandfather).
  - Evaluate point-in-time manifests against retention quotas and mark expired manifests.
- [ ] **Two-Phase Mark-and-Sweep Garbage Collector:**
  - **Phase 1 (Mark):** Traverse all active, retained manifests; construct an LSM-backed live `StorageID` set.
  - **Phase 2 (Sweep):** Scan existing packfile index tables and collect candidate unreferenced chunks.
  - **Epoch Safety Guard:** Enforce a strict minimum grace period ($T_{\text{grace}} \ge 24\text{ hours}$) based on chunk creation timestamps to resolve concurrent backup race conditions.
- [ ] **Packfile Compaction Routine:**
  - Calculate dead-chunk fragmentation ratio per packfile.
  - Repack living chunks from packs with $>40\%$ dead space into new packs; atomically update index and purge stale packfiles.

---

## Phase 5: 3-2-1 Cloud Fabric & Immutability Enforcement
**Target Duration:** Weeks 13–15  
**Objective:** Deliver cross-destination replication with cloud immutability to safeguard against ransomware and credential compromise.

- [ ] **Cloud Storage Backend:**
  - Implement S3-compatible driver (`aws-sdk-go-v2`) supporting AWS S3, Backblaze B2, and MinIO.
  - Enable multipart streaming uploads for packfiles.
- [ ] **Ransomware Protection (WORM):**
  - Implement S3 Object Lock configuration (Compliance Mode).
  - Calculate and set retention headers dynamically based on GFS expiration schedules.
- [ ] **3-2-1 Replication Orchestrator:**
  - Primary working storage $\to$ Local staging/cache repository (NVMe/SSD).
  - Background asynchronous synchronization daemon: Local staging $\to$ Offsite cloud object store.
  - Bandwidth throttling, exponential backoff, and failure recovery.

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
- [ ] **Disaster Recovery Automation:**
  - Headless CI/CD container restoring the entire snapshot onto fresh storage using only master passphrase and S3 bucket credentials.