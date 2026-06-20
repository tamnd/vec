package query

import (
	"container/heap"

	"github.com/tamnd/vector/index"
	"github.com/tamnd/vector/storage"
)

// Batch is the columnar unit that flows between operators (spec 10 §6.2). The
// generic spec model is a []Vector keyed by type; vec's read path only ever moves
// positions, distances, point ids, full-precision vectors, and metadata rows, so
// this is a struct-of-arrays specialization of that model. Each column is valid
// for the first NumRows entries, or, when Sel is non-nil, only at the indices in
// Sel[:NumSel] (the selection vector, spec 10 §6.3).
type Batch struct {
	NumRows int
	Sel     []uint16 // selection vector; nil means all NumRows rows are live
	NumSel  int

	Pos     []uint32          // dense internal positions (spec 10 §7.2)
	Dist    []float32         // distance (kNN) or score (hybrid), nearest/highest first
	PointID []uint64          // resolved external point ids (Project fills, spec 10 §13.1)
	Vecs    [][]float32       // full-precision vectors gathered for rerank (spec 10 §7.3)
	Meta    []storage.MetaRow // projected metadata rows (spec 10 §13.2)
}

// newBatch allocates a batch sized for cap rows from the arena's backing store.
func newBatch(capRows int) *Batch {
	return &Batch{
		Pos:  make([]uint32, capRows),
		Dist: make([]float32, capRows),
	}
}

// reset clears the batch for reuse without freeing its backing slices.
func (b *Batch) reset() {
	b.NumRows = 0
	b.Sel = nil
	b.NumSel = 0
}

// livePositions returns the positions that survive the selection vector, in order.
func (b *Batch) livePositions() []uint32 {
	if b.Sel == nil {
		return b.Pos[:b.NumRows]
	}
	out := make([]uint32, b.NumSel)
	for i, idx := range b.Sel[:b.NumSel] {
		out[i] = b.Pos[idx]
	}
	return out
}

// topKHeap is a bounded max-heap of candidates keyed by distance descending, so
// the farthest candidate is at the root and is evicted when the heap overflows k
// (spec 10 §3.2). Ties break by position so two runs of the same query agree
// (spec 10 §3.3, §20.7).
type topKHeap struct {
	data []index.Candidate
	k    int
}

func newTopKHeap(k int) *topKHeap {
	if k < 1 {
		k = 1
	}
	return &topKHeap{data: make([]index.Candidate, 0, k), k: k}
}

func (h *topKHeap) Len() int { return len(h.data) }

// Less orders the heap as a max-heap: the "greater" candidate (farther, or equal
// distance with the larger position) sorts to the root for eviction. NaN sorts
// greatest so NaN candidates are popped first (spec 10 §20.8).
func (h *topKHeap) Less(i, j int) bool {
	a, b := h.data[i], h.data[j]
	if a.Distance != b.Distance {
		// max-heap: farther is "less" in heap terms so it bubbles to the root via Pop.
		return greaterDist(a.Distance, b.Distance)
	}
	return a.Position > b.Position
}

func (h *topKHeap) Swap(i, j int) { h.data[i], h.data[j] = h.data[j], h.data[i] }
func (h *topKHeap) Push(x any)    { h.data = append(h.data, x.(index.Candidate)) }
func (h *topKHeap) Pop() any {
	old := h.data
	n := len(old)
	c := old[n-1]
	h.data = old[:n-1]
	return c
}

// greaterDist reports whether x is "farther" than y for max-heap ordering, with
// NaN treated as the farthest possible value (spec 10 §20.8).
func greaterDist(x, y float32) bool {
	xn, yn := x != x, y != y // NaN check
	switch {
	case xn && yn:
		return false
	case xn:
		return true
	case yn:
		return false
	default:
		return x > y
	}
}

// push offers a candidate to the bounded heap (spec 10 §3.2 push).
func (h *topKHeap) push(c index.Candidate) {
	if len(h.data) < h.k {
		heap.Push(h, c)
		return
	}
	root := h.data[0]
	// Replace the root only if c is strictly nearer, or equal-distance with a
	// smaller position (so the deterministic tie-break keeps lower positions).
	if greaterDist(root.Distance, c.Distance) ||
		(root.Distance == c.Distance && c.Position < root.Position) {
		h.data[0] = c
		heap.Fix(h, 0)
	}
}

// drain empties the heap into a slice sorted nearest-first (distance ascending,
// then position ascending), the result-ordering guarantee of spec 10 §3.2, §20.9.
func (h *topKHeap) drain() []index.Candidate {
	n := len(h.data)
	out := make([]index.Candidate, n)
	for i := n - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(index.Candidate)
	}
	return out
}
