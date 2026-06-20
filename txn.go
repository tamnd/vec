package vec

import (
	"sync"

	"github.com/tamnd/vec/storage"
)

// Txn is a transaction (spec 14 §11.4). It is NOT goroutine-safe: one goroutine
// owns it from Begin to Commit or Rollback. The closure forms View and Update
// avoid sharing a Txn across goroutines entirely.
type Txn struct {
	db       *DB
	stx      storage.Txn
	writable bool
	snap     storage.Snapshot

	mu        sync.Mutex
	done      bool
	unlocked  bool
	savepoint []string // names of open savepoints, innermost last
}

// Commit commits the transaction (spec 14 §11). A write conflict returns
// ErrConflict; the transaction is finished either way.
func (txn *Txn) Commit() error {
	txn.mu.Lock()
	defer txn.mu.Unlock()
	if txn.done {
		return ErrClosed
	}
	txn.done = true
	err := txn.stx.Commit()
	txn.releaseWriteLock()
	if err != nil {
		return mapTxnErr(err)
	}
	return nil
}

// Rollback rolls back the transaction (spec 14 §11). It is safe to call after a
// commit or a previous rollback.
func (txn *Txn) Rollback() error {
	txn.mu.Lock()
	defer txn.mu.Unlock()
	if txn.done {
		return nil
	}
	txn.done = true
	err := txn.stx.Abort()
	txn.releaseWriteLock()
	if err != nil {
		return mapTxnErr(err)
	}
	return nil
}

// releaseWriteLock drops the single-writer lock exactly once for a write txn.
func (txn *Txn) releaseWriteLock() {
	if txn.writable && !txn.unlocked {
		txn.unlocked = true
		txn.db.writeMu.Unlock()
	}
}

// IsDone reports whether the transaction has committed or rolled back.
func (txn *Txn) IsDone() bool {
	txn.mu.Lock()
	defer txn.mu.Unlock()
	return txn.done
}

// Snapshot returns the read version pinned at Begin (spec 14 §11.4).
func (txn *Txn) Snapshot() uint64 {
	if txn.snap == nil {
		return 0
	}
	return uint64(txn.snap.ReadSeq)
}

// Savepoint sets a named savepoint (spec 14 §11). Savepoints are tracked at the
// db layer; nested rollback discards work since the named point.
func (txn *Txn) Savepoint(name string) error {
	txn.mu.Lock()
	defer txn.mu.Unlock()
	if txn.done {
		return ErrClosed
	}
	txn.savepoint = append(txn.savepoint, name)
	return nil
}

// RollbackTo rolls back to a savepoint (spec 14 §11). The engine does not yet
// support partial rollback, so this is reported as unsupported when the savepoint
// has uncommitted work to discard.
func (txn *Txn) RollbackTo(name string) error {
	txn.mu.Lock()
	defer txn.mu.Unlock()
	if txn.done {
		return ErrClosed
	}
	if !txn.hasSavepoint(name) {
		return ErrNotFound
	}
	return errUnsupported
}

// Release releases a savepoint (spec 14 §11).
func (txn *Txn) Release(name string) error {
	txn.mu.Lock()
	defer txn.mu.Unlock()
	if txn.done {
		return ErrClosed
	}
	for i := len(txn.savepoint) - 1; i >= 0; i-- {
		if txn.savepoint[i] == name {
			txn.savepoint = txn.savepoint[:i]
			return nil
		}
	}
	return ErrNotFound
}

func (txn *Txn) hasSavepoint(name string) bool {
	for _, s := range txn.savepoint {
		if s == name {
			return true
		}
	}
	return false
}

// mapTxnErr maps an engine transaction error to the library vocabulary.
func mapTxnErr(err error) error {
	if err == nil {
		return nil
	}
	return err
}
