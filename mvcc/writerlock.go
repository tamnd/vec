package mvcc

import "context"

// WriterLock serializes write transactions on the single-writer path: only one
// write transaction is active at a time, so first-committer-wins is trivially
// satisfied and no commit-time validation is needed (spec 06 §9). Readers never
// touch it, so a reader can never block the writer however long it runs
// (spec 06 §9.1). It is a buffered channel of capacity one, the common Go idiom
// for a mutex that also supports a context-cancelable acquire.
type WriterLock struct {
	ch chan struct{}
}

// NewWriterLock returns an unlocked writer lock.
func NewWriterLock() *WriterLock {
	return &WriterLock{ch: make(chan struct{}, 1)}
}

// Acquire blocks until the lock is held or ctx is done. A burst of waiting
// writers forms the natural queue group commit later batches behind one fsync
// (spec 06 §9.3).
func (l *WriterLock) Acquire(ctx context.Context) error {
	select {
	case l.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryAcquire takes the lock without blocking, reporting whether it succeeded.
func (l *WriterLock) TryAcquire() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release frees the lock for the next writer. It must be called exactly once per
// successful Acquire, on commit or rollback (spec 06 §8.2 steps 13/7b).
func (l *WriterLock) Release() {
	select {
	case <-l.ch:
	default:
	}
}
