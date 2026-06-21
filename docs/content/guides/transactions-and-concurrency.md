---
title: "Transactions and concurrency"
description: "How vec's MVCC snapshot isolation, single-writer model, and savepoints behave, and how to use them from the library."
weight: 30
---

vec is transactional.
A write either lands whole or not at all, a reader sees a stable snapshot for the life of its transaction, and a crash recovers to the last committed state.
It inherits this from the same storage core as [`kv`](https://github.com/tamnd/kv): a write-ahead log with group commit, MVCC snapshot isolation, and crash recovery.

## Snapshot isolation

Every read transaction runs against a snapshot of the database taken when it began.
A concurrent writer can commit new versions while the reader runs; the reader keeps seeing the snapshot it started with, so a long scan never observes a half-applied write.
This is the same model SQLite's WAL mode gives you, and the same one `kv` and `gr` use.

## One writer at a time

vec uses a single-writer model: at most one writable transaction is open across the whole database at once.
Readers never block and never take the write lock; they run concurrently with the writer and with each other.
A second writer waits for the first to finish, up to the busy timeout, then fails with `ErrBusy`.

This keeps the concurrency model simple to reason about.
Many readers, one writer, no write-write conflicts to retry at the row level.

## Read and write helpers

The library wraps the common cases.
`View` runs a function inside a read transaction, `Update` inside a write transaction, and both clean up for you:

```go
// Read: runs against a stable snapshot.
err := db.View(ctx, func(txn *vec.Txn) error {
	p, err := coll.Get(txn, vec.IntID(42))
	if err != nil {
		return err
	}
	fmt.Println(p.Meta["title"])
	return nil
})

// Write: commits if the function returns nil, rolls back on error.
err = db.Update(ctx, func(txn *vec.Txn) error {
	_, err := coll.Upsert(txn, point)
	return err
})
```

If the `Update` function returns an error, the transaction rolls back and the database is untouched.
If it returns nil, vec commits.

## Manual transactions

For finer control, open a transaction with `Begin` and end it yourself:

```go
txn, err := db.Begin(ctx, true) // true = writable
if err != nil {
	return err
}
defer txn.Rollback() // a no-op once Commit has run

if _, err := coll.Upsert(txn, point); err != nil {
	return err // deferred Rollback undoes the write
}
return txn.Commit()
```

`Begin(ctx, false)` opens a read transaction.
Always pair a `Begin` with a `defer txn.Rollback()`: after a successful `Commit` the rollback is a no-op, and on any early return it undoes the partial work.

## Savepoints

A savepoint is a named point inside a transaction you can roll back to without abandoning the whole transaction:

```go
db.Update(ctx, func(txn *vec.Txn) error {
	if _, err := coll.Upsert(txn, a); err != nil {
		return err
	}
	if err := txn.Savepoint("after_a"); err != nil {
		return err
	}
	if _, err := coll.Upsert(txn, b); err != nil {
		// Undo only b, keep a, and carry on.
		return txn.RollbackTo("after_a")
	}
	return nil
})
```

`Savepoint(name)` marks a point, `RollbackTo(name)` undoes everything after it, and `Release(name)` discards the mark once you no longer need it.

## Batch writes

For bulk ingestion, `UpsertBatch` and `DeleteBatch` run their own transaction internally, so you do not wrap them:

```go
ids, err := coll.UpsertBatch(ctx, points)
```

A batch is one transaction: it commits together or fails together.
This is the path the [bulk loader](/guides/bulk-loading/) uses under the hood.
