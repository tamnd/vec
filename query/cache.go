package query

import (
	"container/list"
	"fmt"
	"sync"

	"github.com/tamnd/vec/storage"
)

// planKey identifies a structurally equivalent bound query for the plan cache
// (spec 13 §13.2). Two queries that differ only in their query vector or their
// per-query ef/nprobe pins share a structural plan, so those fields are excluded;
// the planner re-applies the pins in specialize.
type planKey struct {
	coll        uint64
	k           int
	metric      uint8
	hasPred     bool
	predShape   string // a stable description of the predicate's column/op shape
	recallBand  int    // recall target bucketed to a coarse band
	includeDist bool
	project     string
}

// planKeyOf builds the cache key for a bound query. The predicate shape and project
// list are flattened to strings so the key is comparable.
func planKeyOf(collID uint64, q BoundQuery) planKey {
	band := int(q.RecallTarget * 20) // 5% bands
	return planKey{
		coll:        collID,
		k:           q.K,
		metric:      uint8(q.Metric),
		hasPred:     q.Predicate != nil,
		predShape:   predicateShape(q.Predicate),
		recallBand:  band,
		includeDist: q.IncludeDistance,
		project:     joinCols(q.Project),
	}
}

// predicateShape returns a stable fingerprint of a predicate (spec 13 §13.3). The
// storage.Predicate interface is opaque to this package (its methods are
// unexported), so we render its Go value: the Compare/And/Or/IsNull nodes are
// concrete structs with exported fields, so this captures the column ids, operators,
// and literals. Keying on the literals too means different constants get separate
// cache entries, which is sound (a superset of the spec's shape key).
func predicateShape(p storage.Predicate) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%T%v", p, p)
}

func joinCols(cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	out := cols[0]
	for _, c := range cols[1:] {
		out += "," + c
	}
	return out
}

// planCache is a bounded LRU of physical plans keyed by structural shape (spec 13
// §13.2). It is safe for concurrent use so one planner can serve many goroutines.
type planCache struct {
	mu    sync.Mutex
	cap   int
	ll    *list.List
	items map[planKey]*list.Element
}

type planEntry struct {
	key  planKey
	plan PhysicalPlan
}

// newPlanCache returns an LRU holding up to capacity plans; capacity <= 0 disables
// the cache (Plan then always rebuilds).
func newPlanCache(capacity int) *planCache {
	if capacity <= 0 {
		return nil
	}
	return &planCache{
		cap:   capacity,
		ll:    list.New(),
		items: make(map[planKey]*list.Element, capacity),
	}
}

func (c *planCache) get(k planKey) (PhysicalPlan, bool) {
	if c == nil {
		return PhysicalPlan{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*planEntry).plan, true
	}
	return PhysicalPlan{}, false
}

func (c *planCache) put(k planKey, p PhysicalPlan) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		el.Value.(*planEntry).plan = p
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&planEntry{key: k, plan: p})
	c.items[k] = el
	if c.ll.Len() > c.cap {
		c.evict()
	}
}

func (c *planCache) evict() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	c.ll.Remove(el)
	delete(c.items, el.Value.(*planEntry).key)
}

// Len reports the cached plan count, for tests and observability.
func (c *planCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
