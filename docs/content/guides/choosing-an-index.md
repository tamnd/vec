---
title: "Choosing an index"
description: "How the flat, HNSW, IVF, IVF-PQ, and DiskANN index families trade recall, memory, and build time, and which one to build."
weight: 10
---

A vector column with no index runs an exact flat search: every query scans every vector.
That is correct and is the right choice for a small collection, but it gets slow as the collection grows.
An approximate-nearest-neighbor (ANN) index trades a little recall for a large speedup.
vec offers several index families behind one interface, so the query you write does not change when you switch index.

## The families

| Index | Build with `USING` | Best when |
| --- | --- | --- |
| Flat | `flat` | Small collections, or a recall ground truth. Exact, no approximation. |
| HNSW | `hnsw` | The default for in-memory search. High recall at low latency, higher memory. |
| IVF | `ivfflat` | Large collections where memory matters more than the last point of recall. |
| IVF-PQ | `ivfpq` | Very large collections that must fit in memory. Product quantization shrinks the vectors. |
| DiskANN | `diskann` | Collections too large for RAM. The graph lives on disk and is paged in. |

## Declaring an index

In VectorSQL, create the index on the vector column and choose the method with `USING`.
The `WITH` clause sets the build knobs:

```sql
CREATE INDEX docs_emb_idx ON docs USING hnsw (emb) WITH (m = 16, ef_construction = 200);
```

From the CLI you can build or rebuild a collection's index with the `build` and `reindex` subcommands.
From the library, declare the index in the schema and call `db.BuildIndex(ctx, "docs")`.

## HNSW

HNSW builds a navigable small-world graph.
Two knobs matter at build time:

- `m` is the number of neighbors per node. Higher `m` raises recall and memory.
- `ef_construction` is the candidate list size during the build. Higher values build a better graph and take longer.

At query time, `ef` (the search-time candidate list) trades recall for latency without rebuilding.
From the library:

```go
res, err := coll.Query("emb", q).K(10).Ef(128).All(ctx)
```

## IVF and IVF-PQ

IVF partitions the vectors into `nlist` clusters and searches only the `nprobe` clusters nearest the query.
Higher `nprobe` raises recall and latency.
IVF-PQ adds product quantization, which compresses each vector into a short code, so a very large index fits in memory.
Quantization loses precision, so vec reranks the top candidates against the full-precision vectors:

```go
res, err := coll.Query("emb", q).K(10).Nprobe(32).Rerank(100).All(ctx)
```

`Rerank(n)` pulls the top `n` quantized candidates, recomputes their exact distance, and returns the true top-k.
It is the knob that buys back the accuracy quantization gives up.

## DiskANN

DiskANN keeps the graph on disk and pages in only the parts a search touches, so a collection larger than memory still answers queries.
It is the choice when the index does not fit in RAM and you can spend a little more latency on I/O.

## Picking one

Start with flat while the collection is small and you want exact answers.
Move to HNSW when latency matters and the index fits in memory.
Move to IVF-PQ or DiskANN when memory is the binding constraint.
Because the query code does not change, switching is a matter of building a different index and measuring recall against the flat ground truth.
