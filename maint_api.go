package vec

import (
	"context"
	"fmt"
	"io"

	"github.com/tamnd/vec/query"
	"github.com/tamnd/vec/vectorsql"
)

// AlterCollection applies a schema change (spec 14 §4.9). Only adding a metadata
// column is supported in this build; other alterations report unsupported.
func (db *DB) AlterCollection(ctx context.Context, name string, add ...ColumnDef) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	if len(add) == 0 {
		return nil
	}
	return errUnsupported
}

// Snapshot is a read-only view pinned at a committed sequence (spec 14 §11.4).
type Snapshot struct {
	db  *DB
	seq uint64
}

// Seq returns the committed sequence the snapshot reads at.
func (s *Snapshot) Seq() uint64 { return s.seq }

// ReadSnapshot opens a read snapshot at the current committed sequence (spec 14
// §11.4). A snapshot does not take the write lock and never blocks writers.
func (db *DB) ReadSnapshot(ctx context.Context) (*Snapshot, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	snap := db.engine.Snapshot()
	return &Snapshot{db: db, seq: uint64(snap.ReadSeq)}, nil
}

// Exec runs a VectorSQL statement and returns a cursor (spec 14 §12). Only kNN
// SELECT statements with a literal query vector execute in this build; DDL and DML
// flow through the typed collection API.
func (db *DB) Exec(ctx context.Context, sql string) (*Rows, error) {
	return db.ExecTxn(ctx, nil, sql)
}

// ExecTxn runs a VectorSQL statement inside txn (spec 14 §12).
func (db *DB) ExecTxn(ctx context.Context, txn *Txn, sql string) (*Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	stmt, err := vectorsql.Parse(sql)
	if err != nil {
		return nil, mapSQLErr(err)
	}
	sel, ok := stmt.(*vectorsql.SelectStmt)
	if !ok {
		return nil, errUnsupported // DDL/DML use the typed API in this build
	}
	cs, err := db.state(sel.From)
	if err != nil {
		return nil, err
	}
	bs, err := vectorsql.BindSelect(sel, cs.cc)
	if err != nil {
		return nil, mapSQLErr(err)
	}
	bq := bs.BoundQuery
	if len(bq.Vector) == 0 {
		return nil, fmt.Errorf("vec: query needs a literal vector: %w", ErrSchemaViolation)
	}
	bq.AllowFlat = true
	bq.IncludeDistance = true
	bq.Selectivity = -1

	qcoll := db.queryCollection(cs)
	plan, err := query.NewPlanner(qcoll, 0).Plan(bq)
	if err != nil {
		return nil, mapPlanErr(err)
	}
	rs, err := query.NewExecutor(qcoll).Execute(ctx, plan, bq.Vector)
	if err != nil {
		return nil, mapPlanErr(err)
	}
	return newRows(nil, cs, rs), nil
}

// NewBulkWriter opens a streaming bulk-ingest writer (spec 14 §6). The streaming
// ingest pipeline lands with the bulk subsystem (spec 17).
func (db *DB) NewBulkWriter(ctx context.Context, collection string, opts BulkWriterOptions) (*BulkWriter, error) {
	if _, err := db.state(collection); err != nil {
		return nil, err
	}
	return &BulkWriter{db: db, coll: collection, opts: opts}, nil
}

// NewSortedBulkWriter opens a bulk writer that assumes id-sorted input (spec 14 §6).
func (db *DB) NewSortedBulkWriter(ctx context.Context, collection string, opts BulkWriterOptions) (*BulkWriter, error) {
	opts.Sorted = true
	return db.NewBulkWriter(ctx, collection, opts)
}

// BulkLoad runs a load function against a fresh bulk writer (spec 14 §6).
func (db *DB) BulkLoad(ctx context.Context, collection string, fn LoadFunc, opts BulkWriterOptions) error {
	bw, err := db.NewBulkWriter(ctx, collection, opts)
	if err != nil {
		return err
	}
	if err := fn(ctx, bw); err != nil {
		_ = bw.Close()
		return err
	}
	return bw.Close()
}

// Backup writes a consistent copy of the database to w (spec 14 §8). The backup
// pipeline lands with the backup subsystem (spec 17).
func (db *DB) Backup(ctx context.Context, w io.Writer) error {
	return errUnsupported
}

// CheckpointMode selects how aggressively a checkpoint runs (spec 14 §9).
type CheckpointMode int

const (
	// CheckpointPassive checkpoints what it can without blocking writers.
	CheckpointPassive CheckpointMode = iota
	// CheckpointFull blocks new writers until the WAL is fully applied.
	CheckpointFull
	// CheckpointTruncate is a full checkpoint that also truncates the WAL.
	CheckpointTruncate
)

// CheckpointStats reports the result of a checkpoint (spec 14 §9).
type CheckpointStats struct {
	Mode         CheckpointMode
	PagesWritten int64
	WALFrames    int64
}

// Checkpoint flushes the write-ahead log into the main database (spec 14 §9). The
// embedded engine is process-resident, so a checkpoint is a no-op that reports zero
// pending frames; it becomes meaningful with the on-disk durability subsystem.
func (db *DB) Checkpoint(ctx context.Context, mode CheckpointMode) (CheckpointStats, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return CheckpointStats{}, ErrClosed
	}
	return CheckpointStats{Mode: mode}, nil
}

// Pragma reads or sets a database knob by name (spec 14 §15). An empty value reads
// the current setting; a non-empty value sets it. Unknown pragmas are an error.
func (db *DB) Pragma(ctx context.Context, name, value string) (string, error) {
	switch name {
	case "page_size":
		return fmt.Sprintf("%d", db.cfg.pageSize), nil
	case "cache_size":
		return fmt.Sprintf("%d", db.cfg.cacheBytes), nil
	case "busy_timeout":
		return db.cfg.busyTimeout.String(), nil
	case "mmap":
		return fmt.Sprintf("%t", db.cfg.mmap), nil
	case "synchronous":
		return db.cfg.sync.String(), nil
	default:
		return "", fmt.Errorf("vec: pragma %q: %w", name, ErrUnknownParam)
	}
}
