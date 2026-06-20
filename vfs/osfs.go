package vfs

import (
	"os"
	"path/filepath"
	"sync"
)

// OS is the default, all-platforms backend (spec 05): buffered pread/pwrite
// via os.File.ReadAt/WriteAt with explicit fsync/fdatasync. It is well-behaved
// under the Go scheduler because a blocking syscall detaches the P so other
// goroutines keep running.
type OS struct{}

// NewOS returns the default OS-backed filesystem.
func NewOS() *OS { return &OS{} }

// Open implements FS.
func (OS) Open(path string, flags OpenFlags) (File, error) {
	var mode int
	switch {
	case flags&OpenWrite != 0:
		mode = os.O_RDWR
	default:
		mode = os.O_RDONLY
	}
	if flags&OpenCreate != 0 {
		mode |= os.O_CREATE
	}
	if flags&OpenExclusive != 0 {
		mode |= os.O_EXCL
	}
	f, err := os.OpenFile(path, mode, 0o644)
	if err != nil {
		return nil, err
	}
	return &osFile{f: f}, nil
}

// Delete implements FS.
func (OS) Delete(path string, syncDir bool) error {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if syncDir {
		d, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		// Best-effort directory flush so the unlink is durable.
		_ = d.Sync()
	}
	return nil
}

// Exists implements FS.
func (OS) Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ShmMap returns a process-private shared-memory region for the wal-index. The
// OS backend uses anonymous process memory keyed by path+region, which is
// correct for single-process use (the common case); a future build tag can map
// a real -shm file for multi-process WAL sharing.
func (OS) ShmMap(path string, region int, create bool) ([]byte, error) {
	return globalShm.get(path, region, create)
}

type osFile struct {
	f  *os.File
	mu sync.Mutex
}

func (o *osFile) ReadAt(p []byte, off int64) (int, error)  { return o.f.ReadAt(p, off) }
func (o *osFile) WriteAt(p []byte, off int64) (int, error) { return o.f.WriteAt(p, off) }

func (o *osFile) Sync(mode SyncMode) error {
	// Go's os.File.Sync issues fsync (F_FULLFSYNC on macOS). We do not expose a
	// weaker fdatasync separately here; SyncData maps to the same call, which is
	// the safe default. A faster-but-weaker path is a documented future knob.
	return o.f.Sync()
}

func (o *osFile) Truncate(size int64) error { return o.f.Truncate(size) }

func (o *osFile) Size() (int64, error) {
	fi, err := o.f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// Lock and Unlock are no-ops in the OS backend's default single-process mode;
// the WAL coordinates writers in-process. Real advisory file locking is a
// documented addition for multi-process access.
func (o *osFile) Lock(level LockLevel) error   { return nil }
func (o *osFile) Unlock(level LockLevel) error { return nil }

func (o *osFile) Close() error { return o.f.Close() }
