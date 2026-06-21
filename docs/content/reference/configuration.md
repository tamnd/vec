---
title: "Configuration"
description: "Environment variables, runtime PRAGMA knobs, and the on-disk layout of a .vec database."
weight: 40
---

vec is configured by open-time options in the library, flags on the CLI, and `PRAGMA` statements at runtime.
A handful of environment variables fill in defaults.

## CLI environment variables

| Variable | Meaning |
|----------|---------|
| `VEC_DATABASE` | Default database path when none is given |
| `VEC_MODE` | Default output mode (`table`, `csv`, `json`, ...) |
| `VEC_NULL` | String printed for a NULL value |
| `VEC_BUSY_TIMEOUT` | How long a write waits for the lock before failing busy |
| `NO_COLOR` | Set to any value to disable ANSI color |

## Server environment variables

The [server](/guides/serving-over-the-network/) reads these; a flag overrides the matching variable:

| Variable | Meaning |
|----------|---------|
| `VEC_DATABASE` | Database file to serve |
| `VEC_REST_ADDR` | REST listen address |
| `VEC_GRPC_ADDR` | gRPC listen address |
| `VEC_PG_ADDR` | PostgreSQL wire listen address |
| `VEC_METRICS_ADDR` | Prometheus metrics address |
| `VEC_AUTH_MODE` | `token` or `none` |
| `VEC_TOKEN` | A static API token |
| `VEC_TLS_CERT`, `VEC_TLS_KEY` | TLS certificate and key |
| `VEC_LOG_LEVEL` | `debug`, `info`, `warn`, or `error` |

## Runtime PRAGMA knobs

`PRAGMA name = value` sets a knob for the session; `PRAGMA name` reads it.
The knobs that shape search and storage:

| Knob | Controls |
|------|----------|
| `cache_size` | Buffer-pool budget |
| `page_size` | Page size (fixed at create time) |
| `mmap_size` | Memory-mapped read window |
| `synchronous` | WAL sync level (`off`, `normal`, `full`, `extra`) |
| `hnsw_m`, `hnsw_ef_construction`, `hnsw_ef_search` | HNSW graph and search breadth |
| `ivf_nprobe` | IVF clusters probed per query |
| `rerank_r` | Candidates re-scored at full precision |
| `recall_target` | Target recall the planner aims for |
| `hnsw_build_threads`, `worker_pool_size`, `gomaxprocs` | Build and query parallelism |

`PRAGMA compile_options` lists the build's feature flags, and `PRAGMA table_list` lists collections.

## On-disk layout

A vec database is one file, with optional sidecars while it is open:

```
articles.vec        # the database: vectors, indexes, metadata, catalog, free space
articles.vec-wal    # write-ahead log, folded back at checkpoint
articles.vec-shm    # shared-memory index for the WAL
```

The `-wal` and `-shm` sidecars exist only while the database is open and fold back into the main file at checkpoint, the same way SQLite's do.
To back up or ship a database, copy the `.vec` file after a clean close, or copy all three together while it is open.
