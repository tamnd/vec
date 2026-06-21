---
title: "Filtering and hybrid search"
description: "Narrow a nearest-neighbor query with a scalar WHERE clause, and blend vector distance with full-text scoring in one query."
weight: 20
---

A nearest-neighbor query rarely stands alone.
You want the closest vectors that also belong to one author, fall in a date range, or carry a tag.
vec runs the scalar predicate and the vector search together, so the filter shapes the result instead of trimming it afterward.

## Filter by metadata

In VectorSQL the filter is a normal `WHERE` clause next to the distance `ORDER BY`:

```sql
SELECT id, title
FROM docs
WHERE author = 'alice' AND published
ORDER BY embedding <-> '[0.1, 0.2, 0.3, 0.4]'
LIMIT 10;
```

From the library, chain `.Filter` onto the query builder.
It takes a predicate string with `?` placeholders and the values that fill them:

```go
res, err := coll.Query("embedding", q).
	K(10).
	Filter("author = ? AND published", "alice").
	All(ctx)
```

The placeholders are bound, not interpolated, so a value with a quote or a comma in it is safe.

## How the filter and the search combine

An ANN index returns approximate neighbors, so a naive "search then filter" can return fewer than `k` rows when the filter is selective: the index hands back ten candidates and the `WHERE` clause throws out eight.
vec widens the candidate pool when a filter is present so the result still fills to `k`.
On a selective filter over a large collection this costs a little more work per query, which is the price of getting `k` correct answers instead of two.

For a very selective filter, an exact flat scan over the matching rows can beat the index, since there are few rows to score.
The planner picks between the index and a filtered scan; you write the same query either way.

## Hybrid search: vectors and full text

Hybrid search blends two signals: how close a vector is, and how well the text matches a keyword query.
A document that is both semantically near and contains the search terms should rank above one that is only near.

Declare a full-text index on the text column with the `fts5` method, then a hybrid query scores both:

```sql
CREATE INDEX docs_body_fts ON docs USING fts5 (body);

SELECT id, title
FROM docs
WHERE body MATCH 'vector database'
ORDER BY embedding <-> '[...]'
LIMIT 10;
```

vec combines the vector distance and the text score into one ranking.
The blend leans on both: the keyword match keeps the result on-topic, and the vector distance pulls in semantically close documents the keywords alone would miss.

## Choosing a distance operator

The operator in the `ORDER BY` sets the metric:

| Operator | Metric | Lower is |
| --- | --- | --- |
| `<->` | L2 (Euclidean) | closer |
| `<#>` | inner product | closer (more negative) |
| `<=>` | cosine distance | closer |

Use the operator that matches how the column was declared.
A column built for cosine should be queried with `<=>`; mixing metrics gives meaningless distances.
