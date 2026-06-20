package vec

import (
	"errors"
	"io"
	"time"

	"github.com/tamnd/vec/catalog"
	"github.com/tamnd/vec/storage"
)

// txnNow is the wall-clock timestamp stamped onto DEFAULT NOW() metadata columns
// when a point is lowered. It is a function so a future deterministic clock can
// replace it in tests.
func txnNow() time.Time { return time.Now() }

// txnSnap returns the read snapshot a read uses: the transaction's pinned snapshot
// when a transaction is supplied, otherwise a fresh snapshot at the engine's
// current committed sequence.
func txnSnap(txn *Txn, db *DB) storage.Snapshot {
	if txn != nil && txn.snap != nil {
		return txn.snap
	}
	return db.engine.Snapshot()
}

// classifyWriteErr maps a catalog or engine write error to a library sentinel.
func classifyWriteErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, catalog.ErrDimMismatch):
		return ErrDimMismatch
	case errors.Is(err, catalog.ErrDuplicateKey):
		return ErrAlreadyExists
	case errors.Is(err, catalog.ErrCollectionNotFound):
		return ErrNotFound
	case errors.Is(err, catalog.ErrNaNInVector),
		errors.Is(err, catalog.ErrInfInVector),
		errors.Is(err, catalog.ErrNullViolation),
		errors.Is(err, catalog.ErrTypeMismatch),
		errors.Is(err, catalog.ErrValueOutOfRange),
		errors.Is(err, catalog.ErrCheckViolation),
		errors.Is(err, catalog.ErrUniqueViolation),
		errors.Is(err, catalog.ErrValueTooLarge),
		errors.Is(err, catalog.ErrIDTypeMismatch),
		errors.Is(err, catalog.ErrIDRequired),
		errors.Is(err, catalog.ErrInvalidSchema):
		return ErrSchemaViolation
	case errors.Is(err, catalog.ErrSequenceOverflow):
		return ErrTxnTooBig
	default:
		return err
	}
}

// mapPlanErr maps a planner or executor error to the library vocabulary. The query
// stack already speaks the shared "vec:" sentinels for the cases callers branch on,
// so this is mostly a pass-through with a couple of explicit translations.
func mapPlanErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnknownColumn),
		errors.Is(err, ErrDimMismatch),
		errors.Is(err, ErrSchemaViolation):
		return err
	default:
		return err
	}
}

// BatchOpt tunes a batch write (spec 14 §4.4). Options are additive; the zero value
// is the default behavior.
type BatchOpt func(*batchConfig)

// batchConfig holds resolved batch options.
type batchConfig struct {
	durable bool
}

// WithDurableBatch asks a batch to fsync on commit regardless of the open sync level.
func WithDurableBatch() BatchOpt { return func(c *batchConfig) { c.durable = true } }

// ExportOptions controls Collection.Export (spec 14 §4.4).
type ExportOptions struct {
	// Format is the output encoding: "jsonl" (default), "csv", or "fvecs".
	Format string
	// IncludeVectors writes the stored vectors alongside metadata.
	IncludeVectors bool
	// BatchSize bounds how many points are buffered per flush.
	BatchSize int
}

// BulkWriter is the append-only ingest sink returned by NewBulkWriter (spec 14
// §6, spec 17). The streaming ingest pipeline lands with the bulk subsystem; the
// writer is defined here so the option and loader signatures are stable.
type BulkWriter struct {
	db   *DB
	coll string
	opts BulkWriterOptions
	w    io.Writer
}

// BulkWriterOptions tunes a BulkWriter (spec 14 §6).
type BulkWriterOptions struct {
	// Sorted declares the input is already sorted by id, enabling the fast path.
	Sorted bool
	// BatchSize bounds the points buffered before a flush.
	BatchSize int
	// BuildIndex builds the ANN index as part of the load when true.
	BuildIndex bool
}

// Add appends a point to the bulk stream (spec 14 §6). The streaming ingest path
// is delivered with the bulk subsystem (spec 17).
func (bw *BulkWriter) Add(p Point) error { return errUnsupported }

// Flush flushes buffered points (spec 14 §6).
func (bw *BulkWriter) Flush() error { return errUnsupported }

// Close finalizes the bulk load (spec 14 §6).
func (bw *BulkWriter) Close() error { return errUnsupported }

// ProfileResult is the per-query profile (spec 14 §12.5). Phase-level timings land
// with the observability subsystem (spec 18).
type ProfileResult struct {
	Plan     string
	Phases   []PhaseStats
	Total    time.Duration
	RowsRead int64
}

// PhaseStats is one timed phase of query execution.
type PhaseStats struct {
	Name     string
	Duration time.Duration
}
