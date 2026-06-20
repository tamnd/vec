package vfs

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Mem is an in-memory FS backend (spec 05). It is fast and deterministic,
// and it can simulate crashes: a Crash snapshot drops every byte that was
// written but not yet flushed past a Sync, modelling power loss. The WAL,
// recovery, and checkpoint code run against it unchanged, which is what makes
// crash testing cheap.
type Mem struct {
	mu    sync.Mutex
	files map[string]*memData
	shm   *shmStore
	// faultAfter, when >0, makes the Nth (and later) Sync calls fail, modelling
	// fsyncgate. 0 disables fault injection.
	faultAfter int
	syncs      int
}

// NewMem returns an empty in-memory filesystem.
func NewMem() *Mem {
	return &Mem{files: map[string]*memData{}, shm: newShmStore()}
}

// memData is the durable+volatile byte image of one file. durable holds bytes
// confirmed by a Sync; live holds the current contents. A simulated crash resets
// live to durable.
type memData struct {
	mu      sync.Mutex
	live    []byte
	durable []byte
}

// Open implements FS.
func (m *Mem) Open(path string, flags OpenFlags) (File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.files[path]
	if !ok {
		if flags&OpenCreate == 0 {
			return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
		}
		d = &memData{}
		m.files[path] = d
	} else if flags&OpenExclusive != 0 && flags&OpenCreate != 0 {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrExist}
	}
	return &memFile{fs: m, path: path, data: d}, nil
}

// Delete implements FS.
func (m *Mem) Delete(path string, syncDir bool) error {
	m.mu.Lock()
	delete(m.files, path)
	m.mu.Unlock()
	m.shm.drop(path)
	return nil
}

// Exists implements FS.
func (m *Mem) Exists(path string) (bool, error) {
	m.mu.Lock()
	_, ok := m.files[path]
	m.mu.Unlock()
	return ok, nil
}

// ShmMap implements FS using this instance's private region store.
func (m *Mem) ShmMap(path string, region int, create bool) ([]byte, error) {
	return m.shm.get(path, region, create)
}

// SetSyncFault makes the nth Sync (1-based) and every later Sync return an
// error, simulating an fsync failure. Pass 0 to disable.
func (m *Mem) SetSyncFault(nth int) {
	m.mu.Lock()
	m.faultAfter = nth
	m.syncs = 0
	m.mu.Unlock()
}

// Crash discards all unsynced writes across every file, modelling a power
// failure: each file reverts to the bytes last made durable by a Sync. Shared
// memory (the wal-index) is also dropped, as it would be after a process exit.
func (m *Mem) Crash() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for path, d := range m.files {
		d.mu.Lock()
		d.live = append([]byte(nil), d.durable...)
		d.mu.Unlock()
		m.shm.drop(path)
	}
}

type memFile struct {
	fs   *Mem
	path string
	data *memData
}

func (f *memFile) ReadAt(p []byte, off int64) (int, error) {
	f.data.mu.Lock()
	defer f.data.mu.Unlock()
	if off >= int64(len(f.data.live)) {
		return 0, io.EOF
	}
	n := copy(p, f.data.live[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *memFile) WriteAt(p []byte, off int64) (int, error) {
	f.data.mu.Lock()
	defer f.data.mu.Unlock()
	end := int(off) + len(p)
	if end > len(f.data.live) {
		grown := make([]byte, end)
		copy(grown, f.data.live)
		f.data.live = grown
	}
	copy(f.data.live[off:], p)
	return len(p), nil
}

func (f *memFile) Sync(mode SyncMode) error {
	f.fs.mu.Lock()
	f.fs.syncs++
	fault := f.fs.faultAfter > 0 && f.fs.syncs >= f.fs.faultAfter
	f.fs.mu.Unlock()
	if fault {
		return fmt.Errorf("vec/vfs: injected sync fault on %q", f.path)
	}
	f.data.mu.Lock()
	f.data.durable = append([]byte(nil), f.data.live...)
	f.data.mu.Unlock()
	return nil
}

func (f *memFile) Truncate(size int64) error {
	f.data.mu.Lock()
	defer f.data.mu.Unlock()
	if size < int64(len(f.data.live)) {
		f.data.live = f.data.live[:size]
	} else {
		grown := make([]byte, size)
		copy(grown, f.data.live)
		f.data.live = grown
	}
	return nil
}

func (f *memFile) Size() (int64, error) {
	f.data.mu.Lock()
	defer f.data.mu.Unlock()
	return int64(len(f.data.live)), nil
}

func (f *memFile) Lock(level LockLevel) error   { return nil }
func (f *memFile) Unlock(level LockLevel) error { return nil }
func (f *memFile) Close() error                 { return nil }
