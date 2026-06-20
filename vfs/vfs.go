// Package vfs is the file-I/O seam for vec (spec 05). Every read and write the
// database performs goes through an FS/File pair, so the actual backend
// (buffered syscalls, mmap, in-memory) is swappable and testable. The WAL,
// recovery, and checkpoint logic all speak only to these interfaces, which is
// what lets an in-memory backend drive fast, deterministic crash tests.
package vfs

// OpenFlags controls how a file is opened.
type OpenFlags int

const (
	// OpenRead opens an existing file for reading.
	OpenRead OpenFlags = 1 << iota
	// OpenWrite opens for writing.
	OpenWrite
	// OpenCreate creates the file if it does not exist.
	OpenCreate
	// OpenExclusive requires that the file not already exist (with OpenCreate).
	OpenExclusive

	// OpenReadWrite is the common read+write mode.
	OpenReadWrite = OpenRead | OpenWrite
)

// SyncMode selects the durability of a Sync call.
type SyncMode int

const (
	// SyncData flushes file data (fdatasync): contents are durable, metadata such
	// as mtime may lag. This is the WAL's normal flush.
	SyncData SyncMode = iota
	// SyncFull flushes data and metadata (fsync / F_FULLFSYNC). Used when the file
	// size itself must be durable, e.g. after growth at checkpoint.
	SyncFull
)

// LockLevel is a rung on the journal-mode locking ladder (SQLite-style). WAL
// mode uses shared-memory coordination instead and only takes these around
// checkpoints, but the seam exposes the full ladder so the rollback-journal
// fallback can use it.
type LockLevel int

const (
	LockNone LockLevel = iota
	LockShared
	LockReserved
	LockPending
	LockExclusive
)

// FS is a filesystem namespace: it opens files and answers existence queries.
type FS interface {
	// Open opens or creates a file according to flags.
	Open(path string, flags OpenFlags) (File, error)
	// Delete removes a file. If syncDir is set, the containing directory is
	// flushed so the unlink is durable.
	Delete(path string, syncDir bool) error
	// Exists reports whether path names an existing file.
	Exists(path string) (bool, error)
	// ShmMap returns a shared-memory region backing the -shm wal-index (spec 05).
	// region is a 0-based region index; create asks for the region to be grown
	// into existence. Backends that do not support real shared memory return a
	// process-private region, which is correct for single-process use.
	ShmMap(path string, region int, create bool) ([]byte, error)
}

// File is an open file with positional I/O and explicit durability control.
type File interface {
	// ReadAt reads len(p) bytes at off. It follows io.ReaderAt semantics.
	ReadAt(p []byte, off int64) (int, error)
	// WriteAt writes len(p) bytes at off. It follows io.WriterAt semantics.
	WriteAt(p []byte, off int64) (int, error)
	// Sync flushes buffered writes to stable storage at the requested level. A
	// failed Sync is fatal to durability (fsyncgate, spec 05): callers must not
	// retry and assume success.
	Sync(mode SyncMode) error
	// Truncate sets the file size.
	Truncate(size int64) error
	// Size reports the current file size in bytes.
	Size() (int64, error)
	// Lock advances to a lock level on the journal-mode ladder.
	Lock(level LockLevel) error
	// Unlock drops back to a lock level.
	Unlock(level LockLevel) error
	// Close releases the file. It does not imply Sync.
	Close() error
}

// ShmRegionSize is the granularity of a shared-memory region for the wal-index.
// SQLite uses 32 KiB regions; vec matches that so the -shm layout is familiar.
const ShmRegionSize = 32 * 1024
