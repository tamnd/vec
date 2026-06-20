// Package storage implements the vec storage engine (spec 04): fixed-stride
// columnar vector segments, a separate columnar metadata store, and an id-map
// that mediates between stable application point ids and dense engine positions.
//
// The engine owns the single copy of every vector (spec 04 §2.1, invariant I-1):
// one slot in one segment backs every index and every flat scan. Indexes
// ([07], [08]) operate on positions and call back through FetchVector/ScanVectors
// to read that single copy; they never persist their own vector bytes.
//
// This build keeps segments, columns, and the id-map resident in memory with the
// exact stride math and position addressing the spec mandates (spec 04 §3.2-§3.4),
// so the layout is faithful and the seam to the pager ([05]) is a later slice. The
// in-memory column slices preserve the columnar access pattern (scan one column
// without touching vector bytes, spec 04 §5.1) that the on-disk format also gives.
package storage

import "errors"

// Engine error set (spec 04 §15.5). All errors are wrappable; callers use
// errors.Is and errors.As. The engine never panics on bad input, it returns one
// of these; panics are reserved for structural invariant violations (spec 04
// §25.3), raised inline rather than through a separate assert package.
var (
	ErrNotFound           = errors.New("vec: point id not found")
	ErrDuplicateID        = errors.New("vec: duplicate point id")
	ErrDeleted            = errors.New("vec: position is tombstoned")
	ErrNotVisible         = errors.New("vec: position not visible in snapshot")
	ErrPositionOutOfRange = errors.New("vec: position out of range")
	ErrSegmentCorrupt     = errors.New("vec: segment CRC mismatch")
	ErrDatabaseCorrupt    = errors.New("vec: database requires manual recovery")
	ErrDimensionMismatch  = errors.New("vec: vector dimension does not match collection")
	ErrSchemaMismatch     = errors.New("vec: metadata schema does not match collection")
	ErrCompactionActive   = errors.New("vec: compaction already in progress for collection")

	// ErrUnknownCollection is returned when a collID has no catalog entry.
	ErrUnknownCollection = errors.New("vec: unknown collection")
	// ErrUnknownColumn is returned when a ColID is not part of the collection schema.
	ErrUnknownColumn = errors.New("vec: unknown column")
	// ErrTxnClosed is returned when a committed or aborted transaction is reused.
	ErrTxnClosed = errors.New("vec: transaction already finished")
)
