---
title: "vec"
description: "vec is an embedded single-file vector database in pure Go with the look and feel of SQLite. Vectors, ANN indexes, scalar columns, the catalog, and free space all live in one self-describing .vec file you open with a path and a line of code."
heroTitle: "A vector database in one file"
heroLead: "vec puts the vectors, the ANN indexes, the scalar metadata, the catalog, and the free space into a single self-describing .vec file. Open it with a path, upsert points, build an index, and run filtered nearest-neighbor queries, all from one pure-Go binary or one import."
heroPrimaryURL: "/getting-started/quick-start/"
heroPrimaryText: "Get started"
---

Most vector databases are a server you run, a cluster you operate, and a client you talk to over the network.
vec is a file.
It borrows SQLite's shape: the whole database lives in one `.vec` file with an optional `-wal` and `-shm` sidecar, you open it with a path, and there is no process to manage.

Embed it as a Go library, or use the `vec` command-line tool to load data and run queries:

```bash
vec articles.vec "SELECT id FROM articles ORDER BY embedding <-> :q LIMIT 10"
```

## What it does

- **One self-describing file.** Vectors, ANN indexes, scalar columns, the catalog, and free space share one paged file. Copy it, back it up, or ship it like any other file.
- **Pluggable ANN indexes.** HNSW, IVF, IVF-PQ, Vamana/DiskANN, and SPANN sit behind one index seam, alongside an exact flat index for ground truth. Pick the one that fits your recall and memory budget.
- **Quantization with rerank.** Scalar and product quantization shrink the index, then a full-precision rerank pass restores the top-k accuracy a quantized search would lose.
- **Filtered and hybrid search.** Combine a nearest-neighbor query with a scalar `WHERE` clause, or blend vector distance with full-text scoring, in one query.
- **Real transactions.** MVCC snapshot isolation, a write-ahead log, group commit, and crash recovery, the same durability lineage as [`kv`](https://github.com/tamnd/kv) and `gr`.
- **VectorSQL.** A small SQL dialect with a `<->` distance operator, so a query reads the way a SQL query reads.

## More than a library

The same engine runs three ways. Import it as a Go package, drive it with the `vec` CLI and its interactive shell, or [serve it over the network](/guides/serving-over-the-network/) with REST, gRPC, and the PostgreSQL wire protocol so an existing `psql` or pgvector client can talk to it.

It also reads and writes [encrypted at rest](/guides/encryption-at-rest/), exposes Prometheus metrics and traces, and [bulk-loads](/guides/bulk-loading/) from CSV, JSON, and the `.fvecs`/`.bvecs`/`.ivecs` benchmark formats.

## Where to go next

- New here? Start with the [introduction](/getting-started/introduction/), then the [quick start](/getting-started/quick-start/).
- Want to install it? See [installation](/getting-started/installation/).
- Choosing an index or writing a query? The [guides](/guides/) cover [picking an index](/guides/choosing-an-index/), [filtering and hybrid search](/guides/filtering-and-hybrid-search/), [transactions](/guides/transactions-and-concurrency/), and more.
- Need every flag or every clause? The [reference](/reference/) has the [CLI](/reference/cli/), the [VectorSQL](/reference/vectorsql/) dialect, and the [library API](/reference/library-api/).
