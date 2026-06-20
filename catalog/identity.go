package catalog

import (
	"fmt"
	"sync"

	"github.com/tamnd/vec/storage"
)

// maxIDBytes limits a string or bytes point id (spec 02 §3.2).
const maxIDBytes = 255

// PointID is the application-facing identity of a point in one of its three
// forms (spec 02 §3.2): a uint64, a string, or a byte slice. Exactly one form is
// valid per collection, matching the schema's IDKind.
type PointID struct {
	Kind  IDKind
	U     uint64
	Bytes []byte // string ids are stored as their UTF-8 bytes
}

// BigIntID builds a uint64 point id (spec 02 §3.2).
func BigIntID(n uint64) PointID { return PointID{Kind: IDBigInt, U: n} }

// TextID builds a string point id (spec 02 §3.2).
func TextID(s string) PointID { return PointID{Kind: IDText, Bytes: []byte(s)} }

// BlobID builds a bytes point id (spec 02 §3.2).
func BlobID(b []byte) PointID { return PointID{Kind: IDBlob, Bytes: append([]byte(nil), b...)} }

// String renders a point id for diagnostics and tie-break ordering.
func (p PointID) String() string {
	switch p.Kind {
	case IDBigInt:
		return fmt.Sprintf("%d", p.U)
	default:
		return string(p.Bytes)
	}
}

// engineID folds the point id into the engine's uint64 PointID space (spec 02
// §3.3, §11.3). The engine id-map is keyed by uint64; uint64 ids pass through,
// string and bytes ids fold through an FNV-1a hash. The catalog keeps the
// authoritative id-form table so a hash collision is detected at insert (the
// forward map already holds the colliding original id).
func (p PointID) engineID() storage.PointID {
	if p.Kind == IDBigInt {
		return storage.PointID(p.U)
	}
	return storage.PointID(fnv1a(p.Bytes))
}

func fnv1a(b []byte) uint64 {
	const (
		offset = 1469598103934665603
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime
	}
	return h
}

// validateForm checks that a supplied point id matches the collection's id kind
// (spec 02 §13.17, ID_TYPE_MISMATCH) and respects the length limit (spec 02 §3.2).
func validateForm(want IDKind, p PointID) error {
	if p.Kind != want {
		return fmt.Errorf("%w: collection uses %s ids", ErrIDTypeMismatch, want)
	}
	if want != IDBigInt && len(p.Bytes) > maxIDBytes {
		return fmt.Errorf("%w: id exceeds %d bytes", ErrIDTypeMismatch, maxIDBytes)
	}
	return nil
}

// identity tracks the per-collection point-id state of spec 02 §3.5 and §3.6:
// the auto-increment sequence and the deleted-id set that enforces non-reuse for
// the life of the collection. The set keys on the rendered id form so all three
// id kinds share one structure.
type identity struct {
	mu      sync.Mutex
	kind    IDKind
	auto    bool
	seq     uint64              // next auto-increment value (spec 02 §3.5)
	deleted map[string]struct{} // ids ever assigned, never reused (spec 02 §3.6)
}

func newIdentity(kind IDKind, auto bool) *identity {
	return &identity{kind: kind, auto: auto, deleted: make(map[string]struct{})}
}

// nextAuto returns the next auto-assigned uint64 id (spec 02 §3.5). The counter
// is strictly increasing and never wraps; overflow is an error, not silent
// wraparound (spec 02 §3.10).
func (id *identity) nextAuto() (PointID, error) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if !id.auto {
		return PointID{}, ErrIDRequired
	}
	if id.seq == ^uint64(0) {
		return PointID{}, ErrSequenceOverflow
	}
	n := id.seq
	id.seq++
	return BigIntID(n), nil
}

// reserveAuto advances the sequence past an application-supplied uint64 id so a
// later auto-assignment never collides with it (spec 02 §3.5 monotonicity).
func (id *identity) reserveAuto(p PointID) {
	if !id.auto || p.Kind != IDBigInt {
		return
	}
	id.mu.Lock()
	if p.U >= id.seq {
		id.seq = p.U + 1
	}
	id.mu.Unlock()
}

// markDeleted records that an id was assigned so it can never be reused (spec 02
// §3.6). The record outlives the point and is cleared only when the collection
// is dropped.
func (id *identity) markDeleted(p PointID) {
	id.mu.Lock()
	id.deleted[p.String()] = struct{}{}
	id.mu.Unlock()
}

// wasDeleted reports whether an id was ever assigned and then deleted (spec 02 §3.6).
func (id *identity) wasDeleted(p PointID) bool {
	id.mu.Lock()
	_, ok := id.deleted[p.String()]
	id.mu.Unlock()
	return ok
}
