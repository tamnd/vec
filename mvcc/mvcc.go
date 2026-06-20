// Package mvcc implements vec's multi-version concurrency control: snapshot
// isolation by default, an optional serializable level, a monotonic commit clock,
// per-key version chains (the delta-over-base model), a watermark oracle that
// governs version reclamation, and first-committer-wins conflict detection
// (spec 06).
//
// The model is shared with the kv and gr siblings (spec 06 §2.1 cites [kv 10] and
// [gr 06] directly): the same u64 commit sequence is the global version clock,
// and a version is visible to a snapshot with read sequence r iff its commit
// sequence is at most r and its writer has committed. What differs in vec is what
// the version chains carry (vectors, metadata column values, id-map entries) and
// that reclamation also frees graph-delta entries and tombstoned ANN positions;
// those higher structures sit in the index and storage layers and consume the
// primitives defined here.
package mvcc

import "sync/atomic"

// CommitSeq is the global monotonic version clock (spec 06 §2.1). Zero is never
// assigned to a committed version; it doubles as "not yet committed" on an
// in-flight delta entry.
type CommitSeq uint64

// TxnID is an opaque transaction identifier, unique within a process lifetime.
type TxnID uint64

// Clock issues commit sequences and transaction ids. Both are strictly
// increasing; the commit sequence is the version clock and the txn id is a
// distinct identity space so an in-flight version can be matched by its writer
// before it has a commit sequence.
type Clock struct {
	seq atomic.Uint64
	txn atomic.Uint64
}

// NewClock returns a clock seeded so the next commit sequence is start+1. The db
// layer seeds it from the header's txn high-water mark on open so sequences are
// never reissued across restarts (spec 06 §2.1).
func NewClock(start CommitSeq) *Clock {
	c := &Clock{}
	c.seq.Store(uint64(start))
	return c
}

// NextSeq returns the next commit sequence. Called once per committing txn at the
// commit point.
func (c *Clock) NextSeq() CommitSeq { return CommitSeq(c.seq.Add(1)) }

// Current returns the highest commit sequence assigned so far. A read-only
// snapshot taken now reads up to and including this value.
func (c *Clock) Current() CommitSeq { return CommitSeq(c.seq.Load()) }

// NextTxn returns a fresh transaction id.
func (c *Clock) NextTxn() TxnID { return TxnID(c.txn.Add(1)) }

// Snapshot captures the read point for a transaction (spec 06 §2.1).
type Snapshot struct {
	ReadSeq  CommitSeq // read up to (inclusive) this commit sequence
	TxnID    TxnID     // the reading/writing transaction's own id
	InFlight []TxnID   // concurrent path: txns in flight at snapshot time
}

// IsVisible reports whether a version with the given commit sequence and origin
// transaction id is visible to this snapshot (spec 06 §2.1). Read-your-own-writes
// always wins; a version from a concurrently in-flight transaction is invisible;
// otherwise the commit sequence must be at or before the read point.
func (s *Snapshot) IsVisible(vSeq CommitSeq, vTxn TxnID) bool {
	if vTxn == s.TxnID {
		return true
	}
	for _, t := range s.InFlight {
		if vTxn == t {
			return false
		}
	}
	return vSeq != 0 && vSeq <= s.ReadSeq
}
