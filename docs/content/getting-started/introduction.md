---
title: "Introduction"
description: "What vec is, how it borrows SQLite's shape, and when a single-file vector database is the right tool."
weight: 10
---

vec is a vector database that lives in one file.
There is no server to run and no cluster to operate.
You open a `.vec` file with a path, the same way you open a SQLite database, and everything the database needs is inside it.

## The SQLite shape

SQLite put a whole relational database into one file and made it a library you embed instead of a server you connect to.
vec applies that idea to vector search.

A single `.vec` file holds the vectors, the approximate-nearest-neighbor (ANN) indexes, the scalar metadata columns, the catalog that describes your collections, and the free space the engine reuses.
There is an optional `-wal` and `-shm` sidecar while the database is open, exactly as SQLite has, and it folds back into the main file at checkpoint.

That shape buys the things a file gives you.
Backups are a copy.
Shipping a prebuilt index to another machine is a file transfer.
There is no connection string, no port, and no daemon in your deployment.

## What is inside

vec is the vector sibling of [`kv`](https://github.com/tamnd/kv), an embedded key/value engine, and `gr`, a graph engine.
It reuses their storage core: a pager with a buffer pool, a write-ahead log with group commit, MVCC snapshot isolation, and crash recovery.
On top of that core it adds the parts a vector database needs.

- SIMD distance kernels for L2, inner product, and cosine, with a scalar fallback that is the correctness oracle.
- A pluggable ANN index seam, so HNSW, IVF, IVF-PQ, Vamana/DiskANN, SPANN, and an exact flat index all present the same interface.
- Quantization with full-precision rerank, so a compressed index keeps its top-k accuracy.
- Metadata filtering, so a query can mix vector distance with a scalar predicate.
- Hybrid search, so vector distance can blend with full-text scoring.
- VectorSQL, a small SQL dialect with a `<->` distance operator.

## When to reach for it

vec fits when you want vector search without standing up infrastructure.
That covers an application that ships with an embedded recall index, a desktop or CLI tool that searches local data, a notebook or batch job that needs reproducible nearest-neighbor results, and a service that wants one file it can snapshot and restore.

When you do need a network endpoint, the same engine [serves over REST, gRPC, and the PostgreSQL wire protocol](/guides/serving-over-the-network/), so the file you developed against becomes the database you serve.

The next page covers [installation](/getting-started/installation/); after that, the [quick start](/getting-started/quick-start/) walks a first run end to end.
