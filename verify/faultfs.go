package verify

import (
	"errors"
	"sync"

	"github.com/tamnd/vec/vfs"
)

// ErrCrashed is returned by a crashed FaultFS for every operation after the
// crash point, modelling a process that has been killed (spec 21 §8.1).
var ErrCrashed = errors.New("verify: simulated crash")

// ErrInjectedSync is returned by a SyncFail fault, modelling fsync returning EIO
// (spec 21 §8.1).
var ErrInjectedSync = errors.New("verify: injected sync failure")

// FaultMode selects which fault a FaultFS injects (spec 21 §8.1). The zero value
// FaultNone passes every operation through to the underlying FS.
type FaultMode int

const (
	// FaultNone is a transparent pass-through used to count syncs and to reopen
	// after a crash.
	FaultNone FaultMode = iota
	// FaultSyncFail makes the targeted Sync return an error and persist nothing,
	// modelling a disk write error (EIO).
	FaultSyncFail
	// FaultSyncDrop makes the targeted Sync return success but silently lose the
	// buffered data, modelling a volatile write buffer dropped on power loss.
	FaultSyncDrop
	// FaultWriteTear keeps only the first TearAt bytes of each write, modelling a
	// torn write at a sector boundary.
	FaultWriteTear
	// FaultWriteLoss acknowledges each write but never buffers it, modelling a
	// write that the device claims it took and then loses.
	FaultWriteLoss
	// FaultCrash makes every operation from CrashAfterSync onward fail with
	// ErrCrashed, modelling the process being killed.
	FaultCrash
)

// FaultConfig configures a FaultFS (spec 21 §8.1, §8.2). A FaultFS buffers
// writes in a volatile layer and only pushes them down to the underlying FS on a
// successful Sync, which is what lets it model loss, tearing, and crashes
// precisely without touching the engine.
type FaultConfig struct {
	Mode FaultMode
	// SyncTarget is the 1-based Sync index a SyncFail or SyncDrop fault hits, and
	// every Sync at or after it. 0 means the first Sync.
	SyncTarget int
	// CrashAfterSync is the number of successful Syncs a FaultCrash allows before
	// the next operation crashes.
	CrashAfterSync int
	// TearAt is the byte count a FaultWriteTear keeps from each write.
	TearAt int
}

// FaultFS wraps a vfs.FS and injects faults on its files (spec 21 §8.1). It
// implements vfs.FS, so swapping it in front of the pager needs no engine
// change. Writes accumulate in a per-file volatile buffer and reach the
// underlying FS only on a clean Sync; a crash or a drop discards the buffer.
type FaultFS struct {
	under vfs.FS
	mu    sync.Mutex
	cfg   FaultConfig
	syncs int  // successful syncs so far
	dead  bool // crashed
}

// NewFaultFS wraps under with the given fault configuration.
func NewFaultFS(under vfs.FS, cfg FaultConfig) *FaultFS {
	return &FaultFS{under: under, cfg: cfg}
}

// SyncCount reports how many syncs have been pushed through, used by the crash
// harness to enumerate crash points.
func (fs *FaultFS) SyncCount() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.syncs
}

// Crashed reports whether the FS has hit its crash point.
func (fs *FaultFS) Crashed() bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.dead
}

// Open implements vfs.FS.
func (fs *FaultFS) Open(path string, flags vfs.OpenFlags) (vfs.File, error) {
	fs.mu.Lock()
	dead := fs.dead
	fs.mu.Unlock()
	if dead {
		return nil, ErrCrashed
	}
	f, err := fs.under.Open(path, flags)
	if err != nil {
		return nil, err
	}
	return &faultFile{fs: fs, under: f, path: path}, nil
}

// Delete implements vfs.FS.
func (fs *FaultFS) Delete(path string, syncDir bool) error {
	fs.mu.Lock()
	dead := fs.dead
	fs.mu.Unlock()
	if dead {
		return ErrCrashed
	}
	return fs.under.Delete(path, syncDir)
}

// Exists implements vfs.FS.
func (fs *FaultFS) Exists(path string) (bool, error) { return fs.under.Exists(path) }

// ShmMap implements vfs.FS.
func (fs *FaultFS) ShmMap(path string, region int, create bool) ([]byte, error) {
	return fs.under.ShmMap(path, region, create)
}

// pending is one buffered write held in the volatile layer.
type pending struct {
	off  int64
	data []byte
}

type faultFile struct {
	fs    *FaultFS
	under vfs.File
	path  string

	mu  sync.Mutex
	buf []pending
}

// WriteAt buffers the write in the volatile layer (spec 21 §8.1). A torn write
// keeps only the first TearAt bytes; a lost write buffers nothing. Nothing
// reaches the underlying file until a clean Sync.
func (f *faultFile) WriteAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	if f.fs.dead {
		f.fs.mu.Unlock()
		return 0, ErrCrashed
	}
	mode, tearAt := f.fs.cfg.Mode, f.fs.cfg.TearAt
	f.fs.mu.Unlock()

	keep := p
	switch mode {
	case FaultWriteLoss:
		return len(p), nil // acknowledged, never buffered
	case FaultWriteTear:
		if tearAt >= 0 && tearAt < len(p) {
			keep = p[:tearAt]
		}
	}
	cp := append([]byte(nil), keep...)
	f.mu.Lock()
	f.buf = append(f.buf, pending{off: off, data: cp})
	f.mu.Unlock()
	return len(p), nil
}

// ReadAt overlays the volatile buffer on the underlying file so a writer reads
// its own un-synced writes.
func (f *faultFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.under.ReadAt(p, off)
	if err != nil && n == 0 {
		// The underlying file may be shorter than the buffered writes; fall back
		// to a zeroed window so the overlay can fill it.
		for i := range p {
			p[i] = 0
		}
		n = len(p)
		err = nil
	}
	f.mu.Lock()
	for _, w := range f.buf {
		lo := w.off
		hi := w.off + int64(len(w.data))
		end := off + int64(len(p))
		if hi <= off || lo >= end {
			continue
		}
		for i := lo; i < hi; i++ {
			if i >= off && i < end {
				p[i-off] = w.data[i-lo]
			}
		}
	}
	f.mu.Unlock()
	return n, err
}

// Sync pushes the volatile buffer to the underlying file, or injects the
// configured fault (spec 21 §8.1). A SyncFail returns an error and persists
// nothing; a SyncDrop returns success and discards the buffer; a crash point
// fails with ErrCrashed.
func (f *faultFile) Sync(mode vfs.SyncMode) error {
	f.fs.mu.Lock()
	if f.fs.dead {
		f.fs.mu.Unlock()
		return ErrCrashed
	}
	cfg := f.fs.cfg
	next := f.fs.syncs + 1
	switch cfg.Mode {
	case FaultCrash:
		if f.fs.syncs >= cfg.CrashAfterSync {
			f.fs.dead = true
			f.fs.mu.Unlock()
			f.drop()
			return ErrCrashed
		}
	case FaultSyncFail:
		target := cfg.SyncTarget
		if target == 0 {
			target = 1
		}
		if next >= target {
			f.fs.mu.Unlock()
			return ErrInjectedSync
		}
	case FaultSyncDrop:
		target := cfg.SyncTarget
		if target == 0 {
			target = 1
		}
		if next >= target {
			f.fs.syncs = next
			f.fs.mu.Unlock()
			f.drop()
			return nil
		}
	}
	f.fs.syncs = next
	f.fs.mu.Unlock()
	return f.flush(mode)
}

// flush writes the buffered pending writes through to the underlying file and
// syncs it. The pending order is preserved unless the mode reorders it.
func (f *faultFile) flush(mode vfs.SyncMode) error {
	f.mu.Lock()
	buf := f.buf
	f.buf = nil
	f.mu.Unlock()
	for _, w := range buf {
		if _, err := f.under.WriteAt(w.data, w.off); err != nil {
			return err
		}
	}
	return f.under.Sync(mode)
}

func (f *faultFile) drop() {
	f.mu.Lock()
	f.buf = nil
	f.mu.Unlock()
}

// Truncate flushes intent immediately; truncation is metadata the buffer model
// does not delay.
func (f *faultFile) Truncate(size int64) error {
	f.fs.mu.Lock()
	dead := f.fs.dead
	f.fs.mu.Unlock()
	if dead {
		return ErrCrashed
	}
	return f.under.Truncate(size)
}

func (f *faultFile) Size() (int64, error)             { return f.under.Size() }
func (f *faultFile) Lock(level vfs.LockLevel) error   { return f.under.Lock(level) }
func (f *faultFile) Unlock(level vfs.LockLevel) error { return f.under.Unlock(level) }
func (f *faultFile) Close() error                     { return f.under.Close() }
