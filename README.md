# vec

A modern, high-performance, low-latency **vector database** in pure Go that looks and feels like SQLite: the whole database, the vectors, the ANN indexes, the scalar metadata columns, the catalog, and the free space, lives in one self-describing `.vec` file with an optional `-wal`/`-shm` sidecar. You open it with a path and a line of code.

`vec` is the vector sibling of [`kv`](https://github.com/tamnd/kv) (an embedded key/value engine) and `gr` (a graph engine). It reuses their durability lineage, a pager with a buffer pool, a write-ahead log, group commit, MVCC snapshot isolation, and crash recovery, and adds the parts a vector database needs: SIMD distance kernels, a pluggable approximate-nearest-neighbor index seam, quantization with full-precision rerank, metadata filtering, and hybrid search.

The promise SQLite makes, "the database is a file you can copy, email, and trust," is the promise `vec` keeps, at a latency level SQLite was never designed for: a target of p50 < 1 ms, p99 < 5 ms, and more than 10k QPS for one million 768-dimensional vectors at recall@10 above 0.95.

## Design stance

- **The file is the database.** One self-describing file, a documented byte format (magic `tamnd vector format 1`), forward-and-backward version negotiation, copy-to-back-up. The WAL and shared-memory index are sidecars that vanish on clean close.
- **One copy of every vector.** Indexes hold positions, not vectors. The flat index is a brute-force oracle; HNSW is the default; IVF-PQ and DiskANN scale out. All plug into one **Index SPI**.
- **Quantization is orthogonal to the index.** Scalar int8, product quantization, OPQ, binary, and fp16 are layered under any index, with a full-precision rerank pass that restores recall.
- **Pure Go, no cgo.** SIMD distance kernels are written in Go assembly with runtime CPU dispatch and a portable fallback. The GC is a design constraint, not an afterthought.

## Status

Implementation in progress, tracking the design specification in `~/notes/Spec/2062` (26 documents). Each subsystem is built bottom-up and documented as it lands in `~/notes/Spec/2062/implementation`. The specification is the source of truth; where an implementation and the spec disagree, the spec is the bug until a doc is updated to match a deliberate change.

| Layer | Package | Spec | Implementation doc |
|-------|---------|------|--------------------|
| On-disk format | `format` | 02, 03, 04 | `implementation/01-format.md` |
| File abstraction | `vfs` | 05 | `implementation/02-pager.md` |
| Pager + buffer pool | `pager` | 05 | `implementation/02-pager.md` |
| Write-ahead log + recovery | `wal` | 05 | `implementation/03-wal.md` |
| MVCC + transactions | `mvcc` | 06 | `implementation/04-mvcc.md` |
| Distance kernels | `distance` | 09, 19 | `implementation/05-distance.md` |
| Quantization | `quant` | 09 | `implementation/06-quant.md` |
| Index SPI + HNSW | `index` | 07 | `implementation/07-index-hnsw.md` |
| IVF / DiskANN | `index` | 08 | `implementation/08-index-ivf-diskann.md` |
| Storage engine | `storage` | 04 | `implementation/09-storage.md` |
| Catalog + data model | `catalog` | 02 | `implementation/10-catalog.md` |
| Query execution + planner | `query` | 10, 13 | `implementation/11-query.md` |
| Filtering + hybrid search | `hybrid` | 11 | `implementation/12-hybrid.md` |
| VectorSQL | `vsql` | 12 | `implementation/13-vsql.md` |
| Integration + library API | `db`, `vec` | 14 | `implementation/14-api.md` |
| CLI | `cmd/vec` | 15 | `implementation/15-cli.md` |
| Server | `server` | 16 | `implementation/16-server.md` |

## Repository

Public, `github.com/tamnd/vec`, binary `vec`, module `github.com/tamnd/vec`. Pure Go, Go 1.23, no cgo on the build path.
