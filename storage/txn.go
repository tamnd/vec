package storage

import "github.com/tamnd/vector/mvcc"

// txn is one storage transaction (spec 04 §13.1, spec 06). Writes apply to the
// in-memory structures immediately but are stamped with version 0 until commit;
// version 0 is invisible to every snapshot (spec 06 §2.1), so uncommitted writes
// are not visible to readers (spec 04 §15.3). Commit assigns the commit sequence
// and back-patches the versions of the slots written by this transaction; abort
// runs the undo log in LIFO order. The engine serializes write transactions
// (single-writer, multi-reader, spec 04 §13.1).
type txn struct {
	eng     *Engine
	id      mvcc.TxnID
	readSeq mvcc.CommitSeq
	write   bool
	done    bool

	stamp []func(seq mvcc.CommitSeq) // back-patch version stamps on commit
	undo  []func()                   // reverse mutations on abort, LIFO
}

// Txn is the transaction handle the engine API takes (spec 04 §15.1). It is a
// pointer so the same handle threads through a multi-statement transaction.
type Txn = *txn

// onCommit registers a version back-patch run when the transaction commits.
func (t *txn) onCommit(f func(seq mvcc.CommitSeq)) { t.stamp = append(t.stamp, f) }

// onAbort registers an undo run if the transaction aborts.
func (t *txn) onAbort(f func()) { t.undo = append(t.undo, f) }

// snapshot returns the read snapshot for this transaction (spec 06 §2.1).
func (t *txn) snapshot() Snapshot {
	return &mvcc.Snapshot{ReadSeq: t.readSeq, TxnID: t.id}
}

// Begin starts a transaction (spec 04 §13.1). A write transaction takes the
// engine's writer lock; it must be released by Commit or Abort.
func (e *Engine) Begin(write bool) Txn {
	t := &txn{eng: e, readSeq: e.clock.Current(), write: write}
	if write {
		e.writerMu.Lock()
		t.id = e.clock.NextTxn()
	}
	return t
}

// Commit publishes the transaction's writes (spec 04 §14.2). It assigns the commit
// sequence and stamps every slot the transaction wrote, making them visible to
// later snapshots, then releases the writer lock.
func (t *txn) Commit() error {
	if t.done {
		return ErrTxnClosed
	}
	t.done = true
	if !t.write {
		return nil
	}
	seq := t.eng.clock.NextSeq()
	t.eng.mu.Lock()
	for _, f := range t.stamp {
		f(seq)
	}
	t.eng.mu.Unlock()
	t.eng.writerMu.Unlock()
	return nil
}

// Abort rolls back the transaction (spec 04 §14.2). The undo log reverses each
// mutation in LIFO order; the writer lock is released.
func (t *txn) Abort() error {
	if t.done {
		return ErrTxnClosed
	}
	t.done = true
	if !t.write {
		return nil
	}
	t.eng.mu.Lock()
	for i := len(t.undo) - 1; i >= 0; i-- {
		t.undo[i]()
	}
	t.eng.mu.Unlock()
	t.eng.writerMu.Unlock()
	return nil
}
