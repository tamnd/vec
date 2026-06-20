package vec

import (
	"errors"
	"fmt"
)

// Sentinel errors form the stable error vocabulary of the library (spec 14 §10.1).
// Each has the numeric code used by the C ABI (spec 14 §13) and the server's JSON
// responses, so a Go caller, a Python binding, and a REST client all branch on the
// same integer. Callers test these with errors.Is and extract structured detail
// with errors.As against the typed error structs below.
var (
	ErrNotFound        = errors.New("vec: not found")
	ErrAlreadyExists   = errors.New("vec: already exists")
	ErrConflict        = errors.New("vec: write conflict")
	ErrReadOnly        = errors.New("vec: read only")
	ErrClosed          = errors.New("vec: closed")
	ErrDimMismatch     = errors.New("vec: dimension mismatch")
	ErrSchemaViolation = errors.New("vec: schema violation")
	ErrCorrupt         = errors.New("vec: corrupt")
	ErrNeedsRecovery   = errors.New("vec: needs recovery")
	ErrBusy            = errors.New("vec: database is busy")
	ErrTxnTooBig       = errors.New("vec: transaction too big")
	ErrOptionConflict  = errors.New("vec: option conflicts with stored value")
	ErrUnknownColumn   = errors.New("vec: unknown column")
	ErrUnknownParam    = errors.New("vec: unknown index param")
	ErrNotTrainable    = errors.New("vec: not enough vectors for training")
	ErrInvalidSparse   = errors.New("vec: invalid sparse vector")
	ErrOutOfOrder      = errors.New("vec: points out of order")
	ErrCanceled        = errors.New("vec: canceled")
	ErrVersionMismatch = errors.New("vec: version mismatch")
	ErrEncrypted       = errors.New("vec: file is encrypted")
)

// errCode maps a sentinel error to its numeric code (spec 14 §10.1, §13.3). The
// codes are positive here; the C ABI negates them. Unknown errors map to internal.
func errCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrNotFound):
		return 1
	case errors.Is(err, ErrAlreadyExists):
		return 2
	case errors.Is(err, ErrConflict):
		return 3
	case errors.Is(err, ErrReadOnly):
		return 4
	case errors.Is(err, ErrClosed):
		return 5
	case errors.Is(err, ErrDimMismatch):
		return 6
	case errors.Is(err, ErrSchemaViolation):
		return 7
	case errors.Is(err, ErrCorrupt):
		return 8
	case errors.Is(err, ErrNeedsRecovery):
		return 9
	case errors.Is(err, ErrBusy):
		return 10
	case errors.Is(err, ErrTxnTooBig):
		return 11
	case errors.Is(err, ErrOptionConflict):
		return 12
	case errors.Is(err, ErrUnknownColumn):
		return 13
	case errors.Is(err, ErrUnknownParam):
		return 14
	case errors.Is(err, ErrNotTrainable):
		return 15
	case errors.Is(err, ErrInvalidSparse):
		return 16
	case errors.Is(err, ErrOutOfOrder):
		return 17
	case errors.Is(err, ErrCanceled):
		return 18
	case errors.Is(err, ErrVersionMismatch):
		return 19
	case errors.Is(err, ErrEncrypted):
		return 20
	default:
		return 99
	}
}

// DimError carries dimension mismatch detail (spec 14 §10.3). It unwraps to
// ErrDimMismatch so callers can both errors.Is and errors.As.
type DimError struct {
	Column   string
	Expected int
	Got      int
}

func (e *DimError) Error() string {
	return fmt.Sprintf("vec: column %q: dimension mismatch: expected %d, got %d",
		e.Column, e.Expected, e.Got)
}

// Unwrap returns ErrDimMismatch.
func (e *DimError) Unwrap() error { return ErrDimMismatch }

// SchemaError carries schema violation detail (spec 14 §10.3).
type SchemaError struct {
	Column string
	Reason string
}

func (e *SchemaError) Error() string {
	if e.Column == "" {
		return fmt.Sprintf("vec: schema violation: %s", e.Reason)
	}
	return fmt.Sprintf("vec: column %q: schema violation: %s", e.Column, e.Reason)
}

// Unwrap returns ErrSchemaViolation.
func (e *SchemaError) Unwrap() error { return ErrSchemaViolation }

// CorruptError carries the corrupted page or offset (spec 14 §10.3).
type CorruptError struct {
	PageNum          uint32
	Offset           int64
	ChecksumExpected uint32
	ChecksumGot      uint32
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("vec: corrupt: page %d at offset %d: checksum expected %08x got %08x",
		e.PageNum, e.Offset, e.ChecksumExpected, e.ChecksumGot)
}

// Unwrap returns ErrCorrupt.
func (e *CorruptError) Unwrap() error { return ErrCorrupt }

// errUnsupported reports a surface that the embedded engine does not yet back.
// The methods that return it are part of the spec 14 surface but are completed by
// later milestones (bulk loading, backup, hybrid fusion, pragmas; specs 17, 18).
var errUnsupported = errors.New("vec: operation not yet supported by this build")
