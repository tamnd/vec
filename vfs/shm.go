package vfs

import (
	"fmt"
	"sync"
)

// shmStore holds process-private shared-memory regions for the wal-index, keyed
// by file path. Within one process every connection to the same database sees
// the same regions, which is the coordination the WAL needs (spec 05).
type shmStore struct {
	mu      sync.Mutex
	regions map[string][][]byte
}

func newShmStore() *shmStore {
	return &shmStore{regions: map[string][][]byte{}}
}

func (s *shmStore) get(path string, region int, create bool) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	regs := s.regions[path]
	if region < len(regs) && regs[region] != nil {
		return regs[region], nil
	}
	if !create {
		return nil, fmt.Errorf("vec/vfs: shm region %d for %q does not exist", region, path)
	}
	for len(regs) <= region {
		regs = append(regs, nil)
	}
	regs[region] = make([]byte, ShmRegionSize)
	s.regions[path] = regs
	return regs[region], nil
}

// drop releases every region for a path (called when the wal-index is reset).
func (s *shmStore) drop(path string) {
	s.mu.Lock()
	delete(s.regions, path)
	s.mu.Unlock()
}

// globalShm backs the OS filesystem's ShmMap. memfs instances each own their own
// store so in-memory databases stay isolated per FS.
var globalShm = newShmStore()
