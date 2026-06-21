package server

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/tamnd/vec"
)

// opState is the lifecycle of an asynchronous admin operation (spec 16 §8.3).
type opState string

const (
	opPending opState = "pending"
	opRunning opState = "running"
	opDone    opState = "done"
	opFailed  opState = "failed"
)

// operation is one tracked admin job: a reindex, vacuum, or backup.
type operation struct {
	ID       string
	State    opState
	Progress float64
	Err      string
}

// opRegistry tracks admin operations by id so a client can poll their status
// after the call that launched them returns (spec 16 §8.3).
type opRegistry struct {
	mu  sync.Mutex
	ops map[string]*operation
	seq uint64
}

func newOpRegistry() *opRegistry {
	return &opRegistry{ops: make(map[string]*operation)}
}

// start records a new pending operation and returns its id.
func (r *opRegistry) start() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("op-%d", r.seq)
	r.ops[id] = &operation{ID: id, State: opPending}
	return id
}

// finish marks an operation done or failed.
func (r *opRegistry) finish(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return
	}
	if err != nil {
		op.State = opFailed
		op.Err = err.Error()
		return
	}
	op.State = opDone
	op.Progress = 1
}

// get reads an operation by id.
func (r *opRegistry) get(id string) (operation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operation{}, false
	}
	return *op, true
}

// runAsync launches fn in the background under a tracked operation id. The
// operation registry records the outcome for a later OperationStatus poll.
func (s *Server) runAsync(fn func() error) string {
	id := s.ops.start()
	s.mu.Lock()
	s.ops.ops[id].State = opRunning
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.ops.finish(id, fn())
	}()
	return id
}

// reindex rebuilds a collection's ANN index (spec 16 §8.3).
func (s *Server) reindex(ctx context.Context, collection string) string {
	return s.runAsync(func() error { return s.db.Reindex(ctx, collection) })
}

// vacuum reclaims space by running a full WAL checkpoint (spec 16 §8.3). The
// engine compacts on checkpoint, so vacuum maps onto it.
func (s *Server) vacuum(ctx context.Context) (int64, error) {
	stats, err := s.db.Checkpoint(ctx, vec.CheckpointFull)
	if err != nil {
		return 0, err
	}
	return stats.PagesWritten, nil
}

// backup writes a consistent snapshot of the database to destPath (spec 16 §8.3).
// The engine's Backup is the on-disk durability path from spec 17; until that
// lands it returns an unsupported error, which this surface passes through.
func (s *Server) backup(ctx context.Context, destPath string) (int64, error) {
	f, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if err := s.db.Backup(ctx, f); err != nil {
		return 0, err
	}
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
