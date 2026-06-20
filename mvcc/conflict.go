package mvcc

import (
	"errors"
	"sync"
)

// ErrWriteConflict is returned when first-committer-wins aborts a transaction
// because a concurrent transaction committed a write to a point id this
// transaction also wrote (spec 06 §5.2).
var ErrWriteConflict = errors.New("mvcc: write-write conflict (first committer wins)")

// ErrSnapshotExpired is returned when a transaction's snapshot is older than the
// oldest retained committed write-set, so its write-set can no longer be
// validated and it must retry with a fresh snapshot (spec 06 §5.5).
var ErrSnapshotExpired = errors.New("mvcc: snapshot expired, retry with a fresh snapshot")

// WriteSet records what a write transaction touched, for first-committer-wins
// conflict detection (spec 06 §5.1). Conflicts are tracked at point-id
// granularity; graph-delta neighbor-list writes are deliberately excluded so
// concurrent pure inserts never serialize on each other.
type WriteSet struct {
	// ModifiedPointIDs are the ids inserted, updated, or deleted; two transactions
	// conflict iff they share one.
	ModifiedPointIDs map[PointID]struct{}
	// InsertedPositions are the positions of new inserts, kept for graph-delta GC
	// attribution (spec 06 §5.1), not for conflict detection.
	InsertedPositions []uint32
}

// NewWriteSet returns an empty write-set.
func NewWriteSet() *WriteSet {
	return &WriteSet{ModifiedPointIDs: make(map[PointID]struct{})}
}

// MarkModified records that the transaction wrote point id (insert, update, or
// delete).
func (ws *WriteSet) MarkModified(id PointID) { ws.ModifiedPointIDs[id] = struct{}{} }

// MarkInserted records a newly allocated position for GC attribution.
func (ws *WriteSet) MarkInserted(pos uint32) {
	ws.InsertedPositions = append(ws.InsertedPositions, pos)
}

// ConflictsWith reports whether ws and other modified a common point id
// (spec 06 §5.1). Graph-delta writes are not part of this test.
func (ws *WriteSet) ConflictsWith(other *WriteSet) bool {
	// Iterate the smaller set for a cheaper intersection.
	a, b := ws, other
	if len(b.ModifiedPointIDs) < len(a.ModifiedPointIDs) {
		a, b = b, a
	}
	for id := range a.ModifiedPointIDs {
		if _, ok := b.ModifiedPointIDs[id]; ok {
			return true
		}
	}
	return false
}

// committedWrite is one recently committed transaction's write-set, retained for
// validation against still-open transactions (spec 06 §5.5).
type committedWrite struct {
	txn      TxnID
	seq      CommitSeq
	writeSet *WriteSet
}

// CommittedRing retains recent committed write-sets so a committing transaction
// can validate its own write-set against everything committed after its read
// point (spec 06 §5.5). Entries are dropped once the watermark advances past
// their CommitSeq: no open transaction can have a read point old enough to need
// them. It is the in-memory conflict index on the concurrent-writer path; on the
// single-writer path it is unused because there is never a concurrent committer
// to validate against (spec 06 §5.2).
type CommittedRing struct {
	mu      sync.Mutex
	entries []committedWrite // ascending CommitSeq
}

// NewCommittedRing returns an empty ring.
func NewCommittedRing() *CommittedRing { return &CommittedRing{} }

// Record appends a freshly committed transaction's write-set. Called under the
// commit step after the seq is assigned (spec 06 §8); appends keep CommitSeq
// ascending.
func (r *CommittedRing) Record(txn TxnID, seq CommitSeq, ws *WriteSet) {
	r.mu.Lock()
	r.entries = append(r.entries, committedWrite{txn: txn, seq: seq, writeSet: ws})
	r.mu.Unlock()
}

// Validate checks ws against every transaction committed after readSeq and
// returns the first conflicting committed TxnID with ErrWriteConflict, or 0 and
// nil if the transaction may commit (spec 06 §5.5). It returns ErrSnapshotExpired
// when readSeq predates the oldest retained write-set, so the comparison cannot
// be made soundly and the caller must retry with a fresh snapshot.
func (r *CommittedRing) Validate(readSeq CommitSeq, ws *WriteSet) (TxnID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) > 0 && r.entries[0].seq > readSeq+1 {
		// The oldest retained write-set committed after readSeq+1, so a writer that
		// committed in (readSeq, entries[0].seq) may have been GC'd already; we cannot
		// prove the absence of a conflict.
		return 0, ErrSnapshotExpired
	}
	for i := range r.entries {
		c := &r.entries[i]
		if c.seq <= readSeq {
			continue // committed before the snapshot; already reflected in the read
		}
		if ws.ConflictsWith(c.writeSet) {
			return c.txn, ErrWriteConflict
		}
	}
	return 0, nil
}

// Prune drops write-sets at or below the watermark; no open transaction can have
// a read point old enough to validate against them (spec 06 §5.5).
func (r *CommittedRing) Prune(watermark CommitSeq) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keep := 0
	for keep < len(r.entries) && r.entries[keep].seq < watermark {
		keep++
	}
	if keep > 0 {
		r.entries = append(r.entries[:0], r.entries[keep:]...)
	}
}
