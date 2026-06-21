package vec

import (
	"fmt"
	"time"
)

// ErrInvalidConfig reports a knob value that fails validation (spec 22 §27). It
// carries the knob name, the offending value, and a human-readable reason so a
// caller can branch on errors.As and still log something useful.
type ErrInvalidConfig struct {
	Knob   string
	Value  any
	Reason string
}

func (e *ErrInvalidConfig) Error() string {
	return fmt.Sprintf("vec: invalid config for %s = %v: %s", e.Knob, e.Value, e.Reason)
}

// ErrPragmaReadOnly reports an attempt to set a read-only PRAGMA (spec 22 §27).
type ErrPragmaReadOnly struct {
	Pragma string
}

func (e *ErrPragmaReadOnly) Error() string {
	return fmt.Sprintf("vec: pragma %s is read-only", e.Pragma)
}

// ErrPragmaImmutable reports an attempt to set a create-time PRAGMA on a database
// that already exists (spec 22 §27). The create-time value in the header wins.
type ErrPragmaImmutable struct {
	Pragma    string
	FileValue string
	NewValue  string
}

func (e *ErrPragmaImmutable) Error() string {
	return fmt.Sprintf("vec: pragma %s is fixed at create time (file has %q, cannot set %q)", e.Pragma, e.FileValue, e.NewValue)
}

// ErrUnknownPragma reports a PRAGMA name that is not in the registry (spec 22 §27).
type ErrUnknownPragma struct {
	Pragma string
}

func (e *ErrUnknownPragma) Error() string {
	return fmt.Sprintf("vec: unknown pragma %s", e.Pragma)
}

// ErrSnapshotTooOld reports a read transaction that outlived max_snapshot_age
// (spec 22 §16.2, §27).
type ErrSnapshotTooOld struct {
	Age    time.Duration
	MaxAge time.Duration
}

func (e *ErrSnapshotTooOld) Error() string {
	return fmt.Sprintf("vec: snapshot age %s exceeds max_snapshot_age %s", e.Age, e.MaxAge)
}
