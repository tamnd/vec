---
title: "Release notes"
description: "What shipped in each vec release."
weight: 50
---

The commit-level history lives in the [git log](https://github.com/tamnd/vec/commits/main) and on the [releases page](https://github.com/tamnd/vec/releases).
This page summarises each version.

## v0.1.0

The first release.
vec is an embedded single-file vector database in pure Go: one `.vec` file holds the vectors, the ANN indexes, the scalar metadata, the catalog, and the free space, opened with a path and a line of code.

- **One self-describing file.** Vectors, indexes, metadata, and the catalog share one paged file with an optional `-wal` and `-shm` sidecar, the SQLite shape applied to vector search.
- **Pluggable ANN indexes.** HNSW, IVF, IVF-PQ, Vamana/DiskANN, and SPANN sit behind one index seam, alongside an exact flat index for ground truth. The query you write does not change when you switch index.
- **Quantization with rerank.** Scalar and product quantization shrink the index, and a full-precision rerank pass restores the top-k accuracy a quantized search would lose.
- **Filtered and hybrid search.** A nearest-neighbor query combines with a scalar `WHERE` clause, and vector distance blends with full-text (`fts5`) scoring in one query.
- **Transactions.** MVCC snapshot isolation, a write-ahead log with group commit, savepoints, and crash recovery, the same storage core as [`kv`](https://github.com/tamnd/kv) and `gr`.
- **VectorSQL.** A small SQL dialect with `<->`, `<#>`, and `<=>` distance operators, run from the CLI, the library, or the PostgreSQL wire protocol.
- **Three faces.** The same engine runs as a Go library, the `vec` CLI with an interactive shell, and a network server speaking REST, gRPC, and the PostgreSQL wire protocol.
- **At-rest encryption.** Page-level encryption with a passphrase (Argon2id) or a raw key, AES-256-GCM or ChaCha20-Poly1305, with passphrase change and key rotation.
- **Bulk loading.** Import and export CSV, JSON, JSON Lines, the `.fvecs`/`.bvecs`/`.ivecs` and `.fbin`/`.ibin` benchmark formats, and NumPy `.npy`, with transparent gzip and zstd.
- **Observability.** Prometheus metrics, structured logs, and tracing spans for queries, transactions, and index builds.
