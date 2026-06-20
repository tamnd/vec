package mvcc

import "sync"

// VersionTable is the delta-over-base engine shared by every per-position version
// chain in vec: vectors, metadata columns, and the id-map (spec 06 §2.2-2.4). It
// is the in-memory overlay above the on-file base. A read walks the chain for a
// key newest-first and returns the first entry visible to the snapshot; if none
// is visible the caller falls through to the base (the checkpointed segment
// value). Reclamation folds entries below the watermark back into the base and
// drops them (spec 06 §6.2).
//
// K is the chain key: a position (uint32) for vectors, a (column,position) pair
// for metadata, a point id for the id-map. V is the payload the entry carries.
// The engine is concurrency-safe; readers take the read lock and never block the
// single writer beyond the brief chain mutation.
type VersionTable[K comparable, V any] struct {
	mu     sync.RWMutex
	chains map[K][]versionEntry[V] // newest-first per key
	staged map[TxnID][]K           // keys an in-flight txn has staged, for commit/abort
}

// versionEntry is one version on a key's chain. CommitSeq 0 marks an in-flight
// write whose seq is stamped at commit (spec 06 §2.2).
type versionEntry[V any] struct {
	seq  CommitSeq
	txn  TxnID
	val  V
	tomb bool
}

// NewVersionTable returns an empty version table ready for use.
func NewVersionTable[K comparable, V any]() *VersionTable[K, V] {
	return &VersionTable[K, V]{
		chains: make(map[K][]versionEntry[V]),
		staged: make(map[TxnID][]K),
	}
}

// Stage records an in-flight write of val at key by txn. The entry is prepended
// so the chain stays newest-first; a second write at the same key by the same txn
// supersedes the first for that txn's own reads (read-your-writes, spec 06 §2.6).
// The seq is 0 until Commit stamps it.
func (t *VersionTable[K, V]) Stage(key K, txn TxnID, val V, tombstone bool) {
	t.mu.Lock()
	t.chains[key] = append([]versionEntry[V]{{txn: txn, val: val, tomb: tombstone}}, t.chains[key]...)
	t.staged[txn] = append(t.staged[txn], key)
	t.mu.Unlock()
}

// Visible returns the value visible to s at key, walking the chain newest-first
// and stopping at the first entry s can see (spec 06 §2.2). ok is false when no
// entry is visible (the caller reads the base) or when the visible entry is a
// tombstone (the value is absent for this snapshot).
func (t *VersionTable[K, V]) Visible(key K, s *Snapshot) (val V, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := range t.chains[key] {
		e := &t.chains[key][i]
		if s.IsVisible(e.seq, e.txn) {
			if e.tomb {
				var zero V
				return zero, false
			}
			return e.val, true
		}
	}
	var zero V
	return zero, false
}

// VisibleEntry is like Visible but also reports whether the visible version is a
// tombstone, distinguishing "deleted at this snapshot" (found, tomb) from "no
// delta, read the base" (notFound). The id-map and the kNN filter need this
// three-way answer (spec 06 §4.1).
func (t *VersionTable[K, V]) VisibleEntry(key K, s *Snapshot) (val V, tomb, found bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := range t.chains[key] {
		e := &t.chains[key][i]
		if s.IsVisible(e.seq, e.txn) {
			return e.val, e.tomb, true
		}
	}
	var zero V
	return zero, false, false
}

// Commit stamps every entry staged by txn with seq, publishing them atomically to
// later snapshots (spec 06 §8). It clears the txn's staging record.
func (t *VersionTable[K, V]) Commit(txn TxnID, seq CommitSeq) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range t.staged[txn] {
		chain := t.chains[key]
		for i := range chain {
			if chain[i].txn == txn && chain[i].seq == 0 {
				chain[i].seq = seq
			}
		}
	}
	delete(t.staged, txn)
}

// Abort drops every in-flight entry staged by txn, leaving no trace for other
// snapshots (spec 06 §7, rollback). Committed entries are untouched.
func (t *VersionTable[K, V]) Abort(txn TxnID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range t.staged[txn] {
		chain := t.chains[key]
		out := chain[:0]
		for _, e := range chain {
			if e.txn == txn && e.seq == 0 {
				continue
			}
			out = append(out, e)
		}
		if len(out) == 0 {
			delete(t.chains, key)
		} else {
			t.chains[key] = out
		}
	}
	delete(t.staged, txn)
}

// GC drops versions no live snapshot can reach: for each chain it keeps the
// newest committed entry at or below the watermark (the effective base) and every
// entry above it, and discards everything older (spec 06 §6.2). A chain whose only
// surviving entry is a tombstone at or below the watermark is removed entirely,
// signaling the storage layer that the position may be reclaimed (spec 06 §2.7).
// In-flight entries (seq 0) are always retained. GC never blocks a reader beyond
// the chain mutation and is safe to call incrementally.
func (t *VersionTable[K, V]) GC(watermark CommitSeq) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, chain := range t.chains {
		// Find the newest committed entry at or below the watermark; entries older
		// than it are unreachable.
		cut := -1
		for i := range chain {
			if chain[i].seq != 0 && chain[i].seq <= watermark {
				cut = i
				break
			}
		}
		if cut < 0 {
			continue // nothing below the watermark; whole chain still reachable
		}
		base := chain[cut]
		if base.tomb {
			// The base is a tombstone below the watermark: if nothing newer survives,
			// the key is fully dead and the position can be reclaimed.
			if cut == 0 {
				delete(t.chains, key)
				continue
			}
		}
		t.chains[key] = chain[:cut+1]
	}
}

// Len reports the number of live keys; used by the GC scheduler's size trigger
// (spec 06 §6.2) and by tests.
func (t *VersionTable[K, V]) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.chains)
}
