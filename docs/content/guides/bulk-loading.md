---
title: "Bulk loading"
description: "Import and export points from CSV, JSON, and the .fvecs/.bvecs/.ivecs benchmark formats with the bulk package."
weight: 60
---

Loading a million vectors one upsert at a time is slow.
The `bulk` package streams a file into a collection through batched upserts, and streams a collection back out to a file.
It reads CSV, JSON, JSON Lines, the SIFT/GIST `.fvecs`/`.bvecs`/`.ivecs` benchmark formats, the `.fbin`/`.ibin` big-ANN formats, and NumPy `.npy`, with transparent `.gz` and `.zst` decompression.

## Import from a file

`Import` streams a reader of a known format into an existing collection.
The collection must already exist, so its vector dimension and metadata types are known before the first row is read:

```go
import "github.com/tamnd/vec/bulk"

f, err := os.Open("vectors.fvecs")
if err != nil {
	log.Fatal(err)
}
defer f.Close()

stats, err := bulk.Import(ctx, db, "docs", bulk.FormatFvecs, f, bulk.ImportOptions{})
if err != nil {
	log.Fatal(err)
}
fmt.Printf("read %d, imported %d, errored %d\n",
	stats.RowsRead, stats.RowsImported, stats.RowsErrored)
```

If you have a path rather than an open reader, `bulk.FormatFromExt(path)` picks the format from the extension, so you do not hard-code it:

```go
format := bulk.FormatFromExt("data/sift.fvecs") // bulk.FormatFvecs
```

It looks through a `.gz` or `.zst` suffix, so `sift.fvecs.gz` still resolves to `fvecs`.

## Import options

`ImportOptions` tunes the run:

| Field | Default | Meaning |
| --- | --- | --- |
| `BatchSize` | `4096` | Points per `UpsertBatch` |
| `OnError` | abort | Skip a bad row or stop the run |
| `Mapping` | by position | Bind source fields to the id, vector, and metadata columns |
| `AllowNonFinite` | `false` | Keep `NaN`/`Inf` elements instead of rejecting the row |
| `MaxErrorsKept` | `100` | How many per-row errors to retain in `ImportStats.Errors` |

`AllowNonFinite` is off on purpose: a `NaN` element silently wrecks recall, so by default a row carrying one is rejected and counted, not imported.
The full error count is always in `RowsErrored`; `Errors` holds a capped sample for inspection.

## Export to a file

`Export` streams every live point of a collection to a writer.
The scan runs at a snapshot pinned when the export begins, so a concurrent write does not tear the output:

```go
out, err := os.Create("docs.jsonl")
if err != nil {
	log.Fatal(err)
}
defer out.Close()

err = bulk.Export(ctx, db, "docs", out, bulk.ExportOptions{
	Format:         bulk.ExportJSONL,
	IncludeVectors: true,
})
```

Three export formats: `jsonl` (the default) and `csv` are the readable interchange forms, and `fvecs` is the raw vector form for feeding a benchmark.
For `fvecs` the vector is always written; for JSONL and CSV, set `IncludeVectors` to write it.

## Why batch

Each batch is one transaction (see [transactions](/guides/transactions-and-concurrency/)), so a 4096-point batch commits once instead of 4096 times.
That is the difference between a load that finishes in seconds and one that fsyncs itself to a crawl.
Raise `BatchSize` for fewer, larger commits when you have the memory; lower it to cap the memory a single batch holds.
