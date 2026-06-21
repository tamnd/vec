---
title: "VectorSQL"
description: "The SQL dialect vec speaks: types, distance operators, CREATE TABLE, CREATE INDEX, and the query clauses."
weight: 20
---

VectorSQL is a small SQL dialect built around vector search.
It reads like SQL because it is SQL, with a distance operator and a vector type added.
The CLI runs it, the library binds it, and the PostgreSQL wire protocol accepts it.

## Types

Vector columns:

| Type | Holds |
| --- | --- |
| `VECTOR(n)` | A dense `n`-dimensional float vector |
| `SPARSEVEC(n)` | A sparse vector over an `n`-dimensional space |
| `MULTIVEC(n)` | A set of `n`-dimensional vectors (multi-vector) |

Scalar metadata columns:

| Type | Holds |
| --- | --- |
| `BIGINT` (also `INT`, `INTEGER`) | A 64-bit integer |
| `FLOAT` / `DOUBLE` | A 64-bit float |
| `REAL` | A 32-bit float |
| `TEXT` (also `VARCHAR`) | A string |
| `BOOLEAN` (also `BOOL`) | A boolean |
| `TIMESTAMP` | A timestamp |
| `JSON` (also `JSONB`) | A JSON document |
| `BLOB` (also `BYTEA`) | Raw bytes |

## Distance operators

The operator in an `ORDER BY` sets the metric and ranks ascending, closest first:

| Operator | Metric |
| --- | --- |
| `<->` | L2 (Euclidean) distance |
| `<#>` | Inner product |
| `<=>` | Cosine distance |

A vector literal is a bracketed list in single quotes: `'[1, 0, 0, 0]'`.

## CREATE TABLE

A collection is a table with one or more vector columns and any number of scalar columns:

```sql
CREATE TABLE docs (
    id        BIGINT PRIMARY KEY,
    title     TEXT,
    published BOOLEAN,
    embedding VECTOR(768)
);
```

## CREATE INDEX

Declare an ANN index on a vector column and pick the method with `USING`.
The `WITH` clause sets the build knobs:

```sql
CREATE INDEX docs_emb ON docs USING hnsw (embedding) WITH (m = 16, ef_construction = 200);
```

`USING` accepts `hnsw`, `ivfflat`, `ivfpq`, `diskann`, `flat`, and `fts5` (a full-text index on a text column).
The [choosing an index](/guides/choosing-an-index/) guide covers which method fits which workload, and which `WITH` knobs each one reads.

## INSERT

```sql
INSERT INTO docs (id, title, published, embedding)
VALUES (1, 'one', true, '[0.1, 0.2, 0.3, 0.4]');
```

## SELECT

A nearest-neighbor query orders by a distance operator and limits the result.
A `WHERE` clause filters on scalar columns alongside the search:

```sql
SELECT id, title
FROM docs
WHERE published AND title <> ''
ORDER BY embedding <-> '[0.1, 0.2, 0.3, 0.4]'
LIMIT 10;
```

For [hybrid search](/guides/filtering-and-hybrid-search/), a `MATCH` predicate against a full-text index blends keyword relevance with vector distance:

```sql
SELECT id FROM docs
WHERE body MATCH 'vector database'
ORDER BY embedding <-> '[...]'
LIMIT 10;
```

## Other statements

VectorSQL also parses `UPDATE`, `DELETE`, `DROP`, `ALTER TABLE`, `COPY`, the transaction statements (`BEGIN`, `COMMIT`, `ROLLBACK`, `SAVEPOINT`), prepared statements (`PREPARE`, `EXECUTE`, `DEALLOCATE`), `EXPLAIN`, and `PRAGMA` for runtime knobs.
Run `EXPLAIN` before a query to see whether the planner chose the index or a filtered scan.
