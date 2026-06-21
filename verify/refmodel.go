package verify

import (
	"sort"
	"sync"

	"github.com/tamnd/vec/distance"
)

// Attrs is a point's metadata, keyed by column name. The reference model keeps
// values opaque; a Predicate decides what they mean.
type Attrs map[string]any

// Predicate is a metadata filter (spec 21 §5.1). It reports whether a point's
// attributes pass. A nil Predicate passes every point.
type Predicate func(Attrs) bool

// ModelPoint is one row in a collection: an integer id, its vector, and its
// metadata. It is the verify-package analogue of vec.Point, kept minimal so the
// reference model has no dependency on the storage engine.
type ModelPoint struct {
	ID    uint64
	Vec   []float32
	Attrs Attrs
}

// ModelResult is one hit from a Query: the point id, its position in the live
// set at query time, and its distance to the query.
type ModelResult struct {
	ID   uint64
	Dist float32
}

// QueryOpts carries the optional knobs of a query (spec 21 §5.1): a metadata
// filter and the ANN effort. The reference model ignores Ef because it always
// runs an exact flat scan; the real collection reads it.
type QueryOpts struct {
	Filter Predicate
	Ef     int
}

// Collection is the contract both the real engine and the reference model
// satisfy (spec 21 §5.1). The conformance and metamorphic drivers depend only on
// this interface, so the real DB is wired in by an adapter in its own tests.
type Collection interface {
	Upsert(p ModelPoint) error
	Delete(id uint64) error
	Get(id uint64) (ModelPoint, bool, error)
	Query(q []float32, k int, opts QueryOpts) ([]ModelResult, error)
}

// RefModel is the in-memory reference implementation of a vec collection
// (spec 21 §5.1). It stores every point in a map and answers queries by flat
// search, so it is obviously correct for exactness and its Query result is the
// ground truth for recall. It is safe for concurrent use.
type RefModel struct {
	mu     sync.RWMutex
	metric distance.Metric
	points map[uint64]ModelPoint
}

// NewRefModel returns an empty reference model that ranks with the given metric.
func NewRefModel(metric distance.Metric) *RefModel {
	return &RefModel{metric: metric, points: map[uint64]ModelPoint{}}
}

// Upsert inserts or replaces a point. A repeated upsert of the same id keeps one
// copy, which is the idempotency the real collection must match (spec 21 §21.1).
func (m *RefModel) Upsert(p ModelPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := ModelPoint{ID: p.ID, Vec: append([]float32(nil), p.Vec...)}
	if p.Attrs != nil {
		cp.Attrs = Attrs{}
		for k, v := range p.Attrs {
			cp.Attrs[k] = v
		}
	}
	m.points[p.ID] = cp
	return nil
}

// Delete removes a point. Deleting an absent id is a no-op, matching the
// engine's tombstone semantics where a delete of a missing point is benign.
func (m *RefModel) Delete(id uint64) error {
	m.mu.Lock()
	delete(m.points, id)
	m.mu.Unlock()
	return nil
}

// Get returns the point and whether it is present.
func (m *RefModel) Get(id uint64) (ModelPoint, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.points[id]
	return p, ok, nil
}

// Query runs an exact flat search over the live, filter-passing points and
// returns the top-k by distance, ties broken by id ascending (spec 21 §10.4) so
// the result is deterministic. This result is the recall ground truth.
func (m *RefModel) Query(q []float32, k int, opts QueryOpts) ([]ModelResult, error) {
	m.mu.RLock()
	cand := make([]ModelResult, 0, len(m.points))
	for id, p := range m.points {
		if opts.Filter != nil && !opts.Filter(p.Attrs) {
			continue
		}
		cand = append(cand, ModelResult{ID: id, Dist: MetricDistance(m.metric, q, p.Vec)})
	}
	m.mu.RUnlock()

	sort.Slice(cand, func(i, j int) bool {
		if cand[i].Dist != cand[j].Dist {
			return cand[i].Dist < cand[j].Dist
		}
		return cand[i].ID < cand[j].ID
	})
	if k >= 0 && k < len(cand) {
		cand = cand[:k]
	}
	return cand, nil
}

// Len reports the number of live points.
func (m *RefModel) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.points)
}

// IDs returns the ids of a result list, for recall measurement.
func IDs(rs []ModelResult) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = int64(r.ID)
	}
	return out
}
