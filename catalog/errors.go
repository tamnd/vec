package catalog

import "errors"

// Data-model error set (spec 02 §13). These are the named conditions the write
// path and the catalog raise; callers match with errors.Is. Each maps to the
// error name the library API ([14] §9) and the VectorSQL response surface use.
var (
	// ErrDuplicateKey is a point id that already exists, live or deleted
	// (spec 02 §13.2, DUPLICATE_KEY). The deleted-id record also triggers it.
	ErrDuplicateKey = errors.New("vec: duplicate point id")
	// ErrDimMismatch is a vector whose length differs from the column dimension
	// (spec 02 §13.3, DIM_MISMATCH).
	ErrDimMismatch = errors.New("vec: vector dimension does not match column")
	// ErrNaNInVector is a vector containing a NaN element (spec 02 §13.4).
	ErrNaNInVector = errors.New("vec: vector contains NaN")
	// ErrInfInVector is a vector containing a +Inf or -Inf element (spec 02 §13.5).
	ErrInfInVector = errors.New("vec: vector contains Inf")
	// ErrNullViolation is a NULL for a NOT NULL column (spec 02 §13.6).
	ErrNullViolation = errors.New("vec: null value in NOT NULL column")
	// ErrTypeMismatch is a value whose kind is not convertible to the column type
	// (spec 02 §13.7, schema-fixed mode only).
	ErrTypeMismatch = errors.New("vec: value type does not match column")
	// ErrValueOutOfRange is an int8 or binary element outside its representable
	// range (spec 02 §4.5).
	ErrValueOutOfRange = errors.New("vec: vector element out of range")
	// ErrCheckViolation is a row failing a declared CHECK constraint (spec 02 §13.8).
	ErrCheckViolation = errors.New("vec: check constraint violated")
	// ErrUniqueViolation is a value conflicting with a UNIQUE constraint (spec 02 §13.9).
	ErrUniqueViolation = errors.New("vec: unique constraint violated")
	// ErrMetricUnsupported is an opclass incompatible with the column element type
	// (spec 02 §13.10, METRIC_UNSUPPORTED).
	ErrMetricUnsupported = errors.New("vec: metric not supported for element type")
	// ErrValueTooLarge is a TEXT, BLOB, or JSON value over the size limit (spec 02 §13.11).
	ErrValueTooLarge = errors.New("vec: metadata value too large")
	// ErrCollectionNotFound is a reference to an unknown collection (spec 02 §13.16).
	ErrCollectionNotFound = errors.New("vec: collection not found")
	// ErrCollectionExists is a CREATE TABLE for a name already in the catalog
	// without IF NOT EXISTS (spec 02 §2.2).
	ErrCollectionExists = errors.New("vec: collection already exists")
	// ErrIDTypeMismatch is a point id whose form does not match the collection's
	// declared id kind (spec 02 §13.17, ID_TYPE_MISMATCH).
	ErrIDTypeMismatch = errors.New("vec: point id type does not match collection")
	// ErrReservedName is a user collection named with the reserved vec_ prefix
	// (spec 02 §2.2).
	ErrReservedName = errors.New("vec: collection name uses the reserved vec_ prefix")
	// ErrInvalidSchema is a schema that violates a structural rule of spec 02 §9.1
	// (no vector column, duplicate column names, bad dimension, no primary key).
	ErrInvalidSchema = errors.New("vec: invalid schema")
	// ErrSequenceOverflow is the auto-increment counter passing 2^64-1 (spec 02 §3.5).
	ErrSequenceOverflow = errors.New("vec: auto-increment sequence overflow")
	// ErrIDRequired is a missing point id on a collection without AUTOINCREMENT
	// (spec 02 §16.4).
	ErrIDRequired = errors.New("vec: point id required")
)
