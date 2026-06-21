---
title: "Quick start"
description: "From an empty terminal to a filtered nearest-neighbor query, with both the CLI and the Go library."
weight: 30
---

This walks the core loop: create a collection in a `.vec` file, upsert a few points, and run a nearest-neighbor query with a metadata filter.
The same example is shown with the `vec` command-line tool and with the Go library.

## With the CLI

Create a collection.
A `.vec` file is created on first write:

```bash
vec docs.vec "CREATE TABLE docs (id BIGINT PRIMARY KEY, title TEXT, embedding VECTOR(4))"
```

Insert a few rows.
A vector literal is a bracketed list in single quotes:

```bash
vec docs.vec "INSERT INTO docs VALUES (1, 'one',   '[1,0,0,0]')"
vec docs.vec "INSERT INTO docs VALUES (2, 'two',   '[0,1,0,0]')"
vec docs.vec "INSERT INTO docs VALUES (3, 'three', '[0,0,1,0]')"
```

Run a nearest-neighbor query.
The `<->` operator is L2 distance, so `ORDER BY embedding <-> :q` returns the closest rows first:

```bash
vec docs.vec "SELECT id, title FROM docs ORDER BY embedding <-> '[1,0,0,0]' LIMIT 2"
```

```
id  title
1   one
2   two
```

Run `vec docs.vec` with no SQL to drop into the interactive shell, where `.tables`, `.indexes`, and `.help` describe the database.

## With the Go library

The same database, opened in process.
Here it is `:memory:`, an ephemeral database; pass a path to persist:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/tamnd/vec"
)

func main() {
	ctx := context.Background()

	db, err := vec.Open(":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// A collection with a 4-dim L2 vector column and a text column.
	err = db.CreateCollection(ctx, vec.CollectionSchema{
		Name: "docs",
		Columns: []vec.ColumnDef{
			{Name: "emb", Type: vec.TypeVector, Dim: 4, Metric: vec.MetricL2},
			{Name: "title", Type: vec.TypeText},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	coll, _ := db.Collection("docs")

	// Upsert a few points.
	_, err = coll.UpsertBatch(ctx, []vec.Point{
		{ID: vec.IntID(1), Vectors: map[string]vec.AnyVector{"emb": {Dense: vec.Vector{1, 0, 0, 0}}}, Meta: map[string]vec.Value{"title": vec.TextValue("one")}},
		{ID: vec.IntID(2), Vectors: map[string]vec.AnyVector{"emb": {Dense: vec.Vector{0, 1, 0, 0}}}, Meta: map[string]vec.Value{"title": vec.TextValue("two")}},
		{ID: vec.IntID(3), Vectors: map[string]vec.AnyVector{"emb": {Dense: vec.Vector{0, 0, 1, 0}}}, Meta: map[string]vec.Value{"title": vec.TextValue("three")}},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Nearest two to [1,0,0,0].
	res, err := coll.Query("emb", vec.Vector{1, 0, 0, 0}).K(2).All(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range res {
		fmt.Printf("id=%d dist=%.3f\n", r.ID.N, r.Distance)
	}
}
```

The query builder chains the search controls: `.K(k)` sets the result count, `.Filter("title = ?", "two")` adds a metadata predicate, and `.All(ctx)` or `.Exec(ctx)` runs it.

## Add an index

The examples above run an exact flat search, correct for a small collection.
For a larger one, declare an ANN index on the vector column and build it, then the same queries use it.
The [choosing an index](/guides/choosing-an-index/) guide covers which index family fits which workload.

## Next

- [Choosing an index](/guides/choosing-an-index/) for HNSW, IVF, IVF-PQ, Vamana, and SPANN.
- [Filtering and hybrid search](/guides/filtering-and-hybrid-search/) for predicates and full-text blending.
- The [VectorSQL reference](/reference/vectorsql/) for the full query dialect.
