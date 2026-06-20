package query

import "errors"

// The executor's error set (spec 10 §23.1). Logic errors are caught at plan or
// Open time; index and storage errors bubble up from the SPI; context errors
// produce partial results (spec 10 §15).
var (
	// ErrDimensionMismatch is returned when the query vector length does not match
	// the collection's vector dimension (spec 10 §20.4). Caught at plan time.
	ErrDimensionMismatch = errors.New("vec: query vector dimension does not match collection")
	// ErrQueryMemoryExceeded is returned when the per-query arena exceeds its limit
	// (spec 10 §14.3).
	ErrQueryMemoryExceeded = errors.New("vec: query exceeded memory limit")
	// ErrIndexSearch wraps a failure from the Index SPI Search method (spec 10 §23.1).
	ErrIndexSearch = errors.New("vec: index search failed")
	// ErrStorageRead wraps a storage engine read failure (spec 10 §23.1).
	ErrStorageRead = errors.New("vec: storage read error")
	// ErrNoIndex is returned when no access path is available and flat scan was not
	// permitted (spec 10 §23.1).
	ErrNoIndex = errors.New("vec: no suitable index for this query")
	// ErrInvalidEfSearch is returned when ef_search is below k (spec 10 §23.1); the
	// executor normally clips it up, so this is reserved for strict mode.
	ErrInvalidEfSearch = errors.New("vec: ef_search must be >= k")
	// ErrInvalidK is returned when k < 1 (spec 10 §23.1).
	ErrInvalidK = errors.New("vec: k must be >= 1")
	// ErrNoCollection is returned when the plan references a collection the executor
	// was not given a binding for.
	ErrNoCollection = errors.New("vec: no collection binding for plan")
)
