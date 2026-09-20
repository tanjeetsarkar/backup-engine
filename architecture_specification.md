# Comprehensive System Architecture Specification

## 1. System Overview

This document specifies the architecture of an enterprise-grade, content-addressable, zero-knowledge backup system. The architecture guarantees:
1. **Deduplication Invariant:** Identical byte sequences are stored exactly once per tenant regardless of file boundaries or shifts.
2. **Zero-Knowledge Security:** The storage provider cannot read plaintext, infer file contents via frequency analysis, or verify plaintext existence (preventing confirmation attacks).
3. **Point-in-Time Immutability:** File system states are represented as immutable Merkle trees governed by Grandfather-Father-Son (GFS) retention rules.
4. **Cloud-Native Resilience:** Chunks are aggregated into packfiles to minimize S3 API amplification and protect against ransomware via S3 Object Lock (Compliance Mode).

```
+---------------------------------------------------------------------------------------+
|                                    CLIENT RUNTIME                                     |
|                                                                                       |
|  [Filesystem Stream]                                                                  |
|          |                                                                            |
|          v                                                                            |
|  [FastCDC Engine] --------> [Zstandard Compression]                                  |
|                                     |                                                 |
|                                     v                                                 |
|                     [Argon2id + HMAC Cryptographic Core]                              |
|                                     |                                                 |
|          +--------------------------+--------------------------+                      |
|          | (Data Path)                                         | (Metadata Path)      |
|          v                                                     v                      |
|  [Packfile Aggregator (~16MB)]                         [Merkle DAG Builder]           |
|          |                                                     |                      |
|          +--------------------------+--------------------------+                      |
|                                     |                                                 |
|                                     v                                                 |
|                     [Local Embedded Index (bbolt)]                                    |
+---------------------------------------------------------------------------------------+
                                      |
         +----------------------------+----------------------------+
         |                                                         |
         v                                                         v
+------------------------------------+   +----------------------------------------------+
|     LOCAL REPOSITORY (NVMe/SSD)    |   |         REMOTE OBJECT STORE (S3/B2)          |
|  - Packfiles (`.pack`)             |   |  - Consolidated Packfiles (`.pack`)          |
|  - Local Cache Indexes (`.idx`)    |   |  - Master Merkle Manifests (Encrypted)       |
|  - WAL / Journal                   |   |  - S3 Object Lock (Compliance Immutability)  |
+------------------------------------+   +----------------------------------------------+
```

---

## 2. Ingestion & Content-Defined Chunking Subsystem

Fixed-size chunking fails upon single-byte insertions or deletions. The system uses **FastCDC** with gear-based rolling hashing and normalized chunking boundaries.

### 2.1 FastCDC Parameters
* **Minimum Chunk Size ($S_{\min}$):** $256\text{ KiB}$ ($262,144\text{ bytes}$)
* **Target Chunk Size ($S_{\text{avg}}$):** $1\text{ MiB}$ ($1,048,576\text{ bytes}$)
* **Maximum Chunk Size ($S_{\max}$):** $4\text{ MiB}$ ($4,194,304\text{ bytes}$)

### 2.2 Gear Rolling Hash Formulation
Given an incoming byte stream $B = b_0, b_1, \dots, b_n$, the 32-bit gear hash $H$ is updated iteratively:
$$H_{i} = (H_{i-1} \ll 1) + \text{GEAR}[b_i]$$
where $\text{GEAR}$ is a 256-element table of predefined, uniformly distributed 32-bit random integers.

To eliminate distribution variance, FastCDC uses two masks:
* In the region $[S_{\min}, S_{\text{avg}})$, test against a stricter mask $M_1$ ($21\text{ bits set}$):
  $$H_i \ \& \ M_1 == 0$$
* In the region $[S_{\text{avg}}, S_{\max})$, test against an easier mask $M_2$ ($19\text{ bits set}$):
  $$H_i \ \& \ M_2 == 0$$
* At $S_{\max}$, an unconditional boundary is declared to enforce the upper limit.

---

## 3. Cryptographic Trust Subsystem

Standard convergent encryption ($K = \text{Hash}(M)$) leaks equality globally and is vulnerable to dictionary attacks. This architecture enforces **Single-Tenant Convergent Encryption with Secret-Key Blinding**.

```
                           +----------------------+
                           |   User Passphrase    |
                           +----------------------+
                                      |
                                      v
                          Argon2id (Salt, T=3, M=64MB)
                                      |
                                      v
                             Master Key (K_M)
                                      |
               +----------------------+----------------------+
               |                                             |
               v                                             v
     HKDF-Expand("data-key")                       HKDF-Expand("meta-key")
               |                                             |
               v                                             v
     Chunk Secret (K_data)                        Metadata Secret (K_meta)
               |                                             |
               v                                             |
     HMAC-SHA256(K_data, CID)                                |
               |                                             |
               v                                             v
        Chunk Key (K_c)                           Manifest Encryption
               |                                             |
               v                                             v
     XChaCha20-Poly1305                            XChaCha20-Poly1305
  (Synthetic Deterministic Nonce)
```

### 3.1 Key Derivation Pipeline
1. **Master Key ($K_M$):**
   $$K_M = \text{Argon2id}(\text{Passphrase}, \text{SystemSalt}, \text{iterations}=3, \text{memory}=65536, \text{parallelism}=4, \text{keylen}=32)$$
2. **Subkey Segregation:**
   $$K_{\text{data}} = \text{HKDF-Expand}(K_M, \text{"backup-chunk-hmac-v1"}, 32)$$
   $$K_{\text{meta}} = \text{HKDF-Expand}(K_M, \text{"backup-metadata-v1"}, 32)$$

### 3.2 Chunk Addressing & Cipher
For each raw chunk payload $P_i$:
1. **Compute Content ID (Plaintext Hash):**
   $$\text{CID}_i = \text{BLAKE3}(P_i)$$
2. **Compute Deterministic Chunk Key:**
   $$K_{c_i} = \text{HMAC-SHA256}(K_{\text{data}}, \text{CID}_i)$$
3. **Compute Tenant Storage Address:**
   $$\text{StorageID}_i = \text{BLAKE3}(\text{HMAC-SHA256}(K_{\text{meta}}, \text{CID}_i))$$
   *Result:* Cloud providers cannot correlate identical data across different tenants, defeating multi-tenant confirmation attacks.
4. **Deterministic Nonce Formulation:**
   $$\text{Nonce}_i = \text{BLAKE3}(K_{c_i} \parallel P_i)[0 \dots 23]$$
5. **Payload Encryption:**
   $$E_i = \text{XChaCha20-Poly1305-Encrypt}(K_{c_i}, \text{Nonce}_i, P_i, \text{AAD}=\text{StorageID}_i)$$

---

## 4. Packfile & Indexing Architecture

To resolve the S3 small-object penalty, chunks are containerized into `.pack` files targeting **16 MiB to 32 MiB**.

### 4.1 Binary Packfile Layout
```
+-------------------------------------------------------------------------------+
| Header: Magic [4B: 0x50 0x41 0x43 0x4B] | Version [2B] | Chunk Count [2B]     |
+-------------------------------------------------------------------------------+
| Chunk 1: [StorageID (32B)] [Nonce (24B)] [Length (4B)] [Ciphertext...]        |
+-------------------------------------------------------------------------------+
| Chunk 2: [StorageID (32B)] [Nonce (24B)] [Length (4B)] [Ciphertext...]        |
+-------------------------------------------------------------------------------+
| ...                                                                           |
+-------------------------------------------------------------------------------+
| Tail Index: Table of [StorageID (32B), Offset (8B), EncryptedLength (4B)]     |
+-------------------------------------------------------------------------------+
| Trailer: Tail Index Offset (8B) | Trailer BLAKE3 Checksum (32B)               |
+-------------------------------------------------------------------------------+
```

### 4.2 Embedded Local Index (`bbolt`)
The client maintains a local embedded B+tree repository:
* `cids` bucket: $\text{CID} \to \text{StorageID}$
* `chunks` bucket: $\text{StorageID} \to \{\text{PackID}, \text{Offset}, \text{Length}, \text{UploadTimestamp}\}$
* `snapshots` bucket: $\text{SnapshotID} \to \text{ManifestEnvelope}$

---

## 5. Point-in-Time Merkle Manifests

Snapshots are immutable Merkle trees representing absolute filesystem state at time $t$.

```
                       [Snapshot Root Manifest]
                                  |
               +------------------+------------------+
               |                                     |
         [Directory: /]                        [Metadata Envelope]
               |
        +------+------+
        |             |
  [Dir: /etc]   [File: hosts]
                      |
           +----------+----------+
           |                     |
     [Chunk Ref 0]         [Chunk Ref 1]
    (StorageID_A)         (StorageID_B)
```

Each snapshot manifest contains:
- `SnapshotID`: BLAKE3 hash of the encrypted payload.
- `ParentSnapshotID`: Identifier of the predecessor snapshot.
- `Timestamp`: Epoch seconds when snapshot ingestion completed.
- `GFS_Tags`: Active retention markers (`DAILY`, `WEEKLY`, `MONTHLY`, `YEARLY`).
- `TreeRoot`: Merkle root covering all file nodes and metadata entries.

### 5.1 Mutable Snapshot Lifecycle Metadata

Immutable snapshot content is managed separately from mutable operator metadata. The bbolt `snapshot_lifecycle` bucket stores metadata-key-encrypted, versioned records containing active/trashed state, pin status, retain-until, labels, notes, and trash/purge timestamps. Editing lifecycle metadata never changes the snapshot ID or Merkle tree.

Snapshots move to recoverable trash before physical deletion. Trashed snapshots remain part of the GC live set until their purge deadline. Active pinned snapshots and active snapshots with a future retain-until deadline override normal GFS expiration.

### 5.2 Backup Commit and Recovery Boundary

Repository mutations use a cross-process one-writer lock. During backup, newly published pack IDs and chunk mappings are recorded in a durable operation journal. Each pack's CID, reverse-CID, and location records are written in one bbolt transaction. Immediately before snapshot commit, cancellation is checked again.

Snapshot insertion and operation-journal deletion occur in one bbolt transaction, defining the commit point. Before that point, cancellation removes operation-owned mappings and packs. If the process terminates, repository open detects the journal and performs the same cleanup idempotently. If the journal is absent and the snapshot exists, commit completed and the snapshot is preserved.

---

## 6. Grandfather-Father-Son (GFS) Rotation & Safe Garbage Collection

### 6.1 GFS Retention Policy Slots
* **Son (Daily):** Retain daily snapshots for 7 days.
* **Father (Weekly):** Retain one snapshot per calendar week for 4 weeks.
* **Grandfather (Monthly):** Retain one snapshot per calendar month for 12 months.
* **Archive (Yearly):** Retain one snapshot per calendar year for $N$ years.

### 6.2 The Concurrent Ingestion / GC Race Condition
* **Problem:** A concurrent backup calculates that Chunk $X$ exists and skips uploading it. Meanwhile, the garbage collector runs, finds Chunk $X$ unreferenced in committed snapshots, and purges it before the new manifest commits.
* **Solution (Epoch Grace Period):**
  $$\text{EligibleForPurge}(C) \iff C \notin \text{LiveSet} \quad \land \quad (T_{\text{current}} - T_{\text{upload}}(C) > T_{\text{grace}})$$
  Where $T_{\text{grace}} \ge 24\text{ hours}$.

```
  Phase 1: MARK
    Traverse all active manifests retained by GFS.
    Populate In-Memory / LSM Live StorageID Set.

  Phase 2: SWEEP
    Iterate over packfile index entries:
    If StorageID not in LiveSet:
      If (Now - UploadTime) > 24 Hours:
         Mark chunk dead inside Packfile Index.
      Else:
         Preserve chunk (In-flight grace protection).

  Phase 3: COMPACT
    For each packfile:
      Dead Space Ratio = (Dead Bytes / Total Bytes)
      If Dead Space Ratio > 0.40:
         Download Packfile.
         Filter surviving chunks into new Packfile buffer.
         Upload new Packfile.
         Atomically update indexes and delete old Packfile.
```

---

## 7. 3-2-1 Cloud Storage Topology & Immutability

Rsync is not part of the repository protocol. It cannot create a transactionally consistent view of the bbolt index and immutable packfiles when copying a live repository. Replication must operate on a consistent checkpoint through the storage abstraction and verify content-addressed packs after transfer.

```
                       [Ingestion Pipeline]
                                |
               +----------------+----------------+
               |                                 |
        (Immediate Sync)                  (Async Buffer)
               v                                 v
     [Copy 2: Local NVMe/SSD]          [Copy 3: Offsite S3 Bucket]
     - Embedded Index                  - S3 Object Lock (Compliance)
     - Packfile Cache                  - Server-Side Versioning Disabled
     - Instant Restores                - Multi-Region Replication
```

1. **3 Copies:** Working filesystem (Copy 1), Local NVMe/SSD pack cache (Copy 2), Offsite cloud bucket (Copy 3).
2. **2 Media Formats:** Local high-throughput block/NVMe storage; remote distributed object store.
3. **1 Offsite with Immutability:** Remote bucket configured with **S3 Object Lock** in `COMPLIANCE` mode. Retain-until dates are set in accordance with GFS policy, preventing deletion even under compromised root cloud credentials.