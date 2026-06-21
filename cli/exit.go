// Package cli implements the vec command-line tool: the interactive shell, the
// batch SQL surface, and the administrative subcommands (spec 15). It is a thin
// layer over the embedded vec library; the shell and every subcommand open a file
// through the same library path and share one exit-code contract.
package cli

import (
	"errors"

	"github.com/tamnd/vec"
)

// Exit codes are the stable scripting contract (spec 15 §10.1). They mirror the
// library's typed errors so a shell script branches on the same integers a Go
// caller branches on with errors.Is.
const (
	ExitOK           = 0  // success
	ExitNotFound     = 1  // no results, or key/point/collection not found
	ExitUsage        = 2  // bad flags, missing arguments, unknown subcommand
	ExitFileNotFound = 3  // file not found or cannot be opened
	ExitCorrupt      = 4  // corruption detected
	ExitBusy         = 5  // file locked by another writer past the timeout
	ExitConflict     = 6  // write could not commit after retries
	ExitEncryption   = 7  // wrong key or AEAD tag mismatch
	ExitIO           = 8  // I/O or durability error
	ExitNetwork      = 9  // network error in serve or bench --dataset URL
	ExitInterrupted  = 10 // interrupted by signal
	ExitSchema       = 11 // schema or dimension mismatch
	ExitParam        = 12 // named parameter missing or type mismatch
)

// exitForError maps a library error to its exit code (spec 15 §10.1). It is the
// single place the CLI translates an error into a process status.
func exitForError(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, vec.ErrNotFound):
		return ExitNotFound
	case errors.Is(err, vec.ErrBusy):
		return ExitBusy
	case errors.Is(err, vec.ErrConflict):
		return ExitConflict
	case errors.Is(err, vec.ErrEncrypted):
		return ExitEncryption
	case errors.Is(err, vec.ErrCorrupt), errors.Is(err, vec.ErrNeedsRecovery):
		return ExitCorrupt
	case errors.Is(err, vec.ErrDimMismatch), errors.Is(err, vec.ErrSchemaViolation):
		return ExitSchema
	case errors.Is(err, vec.ErrCanceled):
		return ExitInterrupted
	case errors.Is(err, vec.ErrReadOnly), errors.Is(err, vec.ErrClosed):
		return ExitUsage
	default:
		return ExitIO
	}
}

// isNotFound reports whether err is a missing collection, index, or point.
func isNotFound(err error) bool { return errors.Is(err, vec.ErrNotFound) }

// isAlreadyExists reports whether err is a duplicate collection or index.
func isAlreadyExists(err error) bool { return errors.Is(err, vec.ErrAlreadyExists) }

// usageError is returned by argument parsing to signal ExitUsage with a message.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// usagef builds a usage error.
func usagef(msg string) error { return &usageError{msg: msg} }
