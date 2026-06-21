---
title: "Library API"
description: "A tour of the Go types you open, write, and query a vec database with."
weight: 30
---

The root package is `github.com/tamnd/vec`.
A `*DB` is the database handle, a `*Collection` is one table of points, a `*Txn` is a transaction, and a `*QueryBuilder` builds a search.
A `*DB` is safe for concurrent use; a `*Txn` and a `*Rows` belong to one goroutine.

```bash
go get github.com/tamnd/vec
```

## Opening

```go
db, err := vec.Open("articles.vec")        // create or open a file
db, err := vec.Open(":memory:")            // ephemeral, in-process
db, err := vec.OpenReadOnly("articles.vec")
db, err := vec.OpenDSN("file:articles.vec?mode=ro")
defer db.Close()
```

`Open` takes options: `vec.WithCacheSize`, `vec.WithSynchronous`, `vec.WithBusyTimeout`, `vec.WithReadOnly`, `vec.WithParallelism`, `vec.WithPassphrase`, `vec.WithEncryptionKey`, `vec.WithCipher`, `vec.WithLogger`, `vec.WithMetrics`, and `vec.WithTracer`.

## Creating a collection

```go
err := db.CreateCollection(ctx, vec.CollectionSchema{
	Name: "articles",
	Columns: []vec.ColumnDef{
		{Name: "embedding", Type: vec.TypeVector, Dim: 768, Metric: vec.MetricCosine},
		{Name: "author", Type: vec.TypeText},
	},
})
coll, err := db.Collection("articles")
```

The metric is one of `vec.MetricL2`, `vec.MetricCosine`, `vec.MetricDot`, `vec.MetricHamming`, or `vec.MetricJaccard`.

## Points and values

A `Point` carries an id, one or more vectors keyed by column name, and scalar metadata:

```go
p := vec.Point{
	ID:      vec.IntID(1),
	Vectors: map[string]vec.AnyVector{"embedding": {Dense: vec.Vector{0.1, 0.2, 0.3}}},
	Meta:    map[string]vec.Value{"author": vec.TextValue("alice")},
}
```

Value constructors: `vec.IntValue`, `vec.FloatValue`, `vec.BoolValue`, `vec.TextValue`, and `vec.JSONValue`.

## Writing

Batched writes run their own transaction and are the path for ingestion:

```go
ids, err := coll.UpsertBatch(ctx, points)
err = coll.DeleteBatch(ctx, []vec.PointID{vec.IntID(1)})
```

Single writes take a transaction (see [transactions](/guides/transactions-and-concurrency/)):

```go
err := db.Update(ctx, func(txn *vec.Txn) error {
	_, err := coll.Upsert(txn, p)
	return err
})
```

## Building an index

```go
err := db.BuildIndex(ctx, "articles")
```

The index method and its build knobs come from the schema or a `CREATE INDEX` statement.

## Querying

The query builder chains the search controls and ends in a runner:

```go
res, err := coll.Query("embedding", q).
	K(10).                          // top-k
	Filter("author = ?", "alice").  // metadata predicate, bound args
	Ef(128).                        // HNSW search breadth
	Nprobe(32).                     // IVF clusters to probe
	Rerank(100).                    // re-score top-100 at full precision
	All(ctx)

for _, r := range res {
	fmt.Println(r.ID, r.Distance)
}
```

Runners: `All` returns a slice of `Result`, `First` returns the single closest, and `Exec` returns a `*Rows` cursor to stream large results.
A `Result` carries `ID`, `Distance`, `Score` (for hybrid queries), and the requested `Point` columns.
`Explain` returns the plan as text, and `Profile` returns per-stage timings.

For hybrid search, `BM25(field, text)` adds a keyword signal and `RRF(k)` fuses the two rankings with reciprocal-rank fusion.

## Sparse and multi-vector

`coll.SparseQuery(column, sparse)` and `coll.MultiQuery(column, multi)` query sparse and multi-vector columns with the same builder.
