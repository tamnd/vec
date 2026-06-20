package index

// The search heaps (spec 07 §4.2). The candidate frontier is a min-heap (explore
// the closest unexplored node first); the bounded result set is a max-heap (evict
// the farthest when full). Ties break by position so a given query against a given
// graph always returns the same ordering (spec 07 §15.2).

// minHeap orders Candidates ascending by distance, then ascending by position.
type minHeap []Candidate

func minLess(a, b Candidate) bool {
	if a.Distance != b.Distance {
		return a.Distance < b.Distance
	}
	return a.Position < b.Position
}

func (h *minHeap) push(c Candidate) {
	*h = append(*h, c)
	i := len(*h) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if minLess((*h)[i], (*h)[parent]) {
			(*h)[i], (*h)[parent] = (*h)[parent], (*h)[i]
			i = parent
		} else {
			break
		}
	}
}

func (h *minHeap) pop() Candidate {
	old := *h
	n := len(old)
	top := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	h.down(0)
	return top
}

func (h *minHeap) down(i int) {
	n := len(*h)
	for {
		l, r := 2*i+1, 2*i+2
		smallest := i
		if l < n && minLess((*h)[l], (*h)[smallest]) {
			smallest = l
		}
		if r < n && minLess((*h)[r], (*h)[smallest]) {
			smallest = r
		}
		if smallest == i {
			return
		}
		(*h)[i], (*h)[smallest] = (*h)[smallest], (*h)[i]
		i = smallest
	}
}

func (h minHeap) peek() Candidate { return h[0] }
func (h minHeap) len() int        { return len(h) }

// maxHeap orders Candidates descending by distance, then descending by position,
// so the top is the farthest (and, among equal distances, the larger position):
// evicting it keeps the smaller-position candidate, matching the tie-break rule.
type maxHeap []Candidate

func maxLess(a, b Candidate) bool {
	if a.Distance != b.Distance {
		return a.Distance > b.Distance
	}
	return a.Position > b.Position
}

func (h *maxHeap) push(c Candidate) {
	*h = append(*h, c)
	i := len(*h) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if maxLess((*h)[i], (*h)[parent]) {
			(*h)[i], (*h)[parent] = (*h)[parent], (*h)[i]
			i = parent
		} else {
			break
		}
	}
}

func (h *maxHeap) pop() Candidate {
	old := *h
	n := len(old)
	top := old[0]
	old[0] = old[n-1]
	*h = old[:n-1]
	h.down(0)
	return top
}

func (h *maxHeap) down(i int) {
	n := len(*h)
	for {
		l, r := 2*i+1, 2*i+2
		largest := i
		if l < n && maxLess((*h)[l], (*h)[largest]) {
			largest = l
		}
		if r < n && maxLess((*h)[r], (*h)[largest]) {
			largest = r
		}
		if largest == i {
			return
		}
		(*h)[i], (*h)[largest] = (*h)[largest], (*h)[i]
		i = largest
	}
}

func (h maxHeap) peek() Candidate { return h[0] }
func (h maxHeap) len() int        { return len(h) }

// SliceBitmap is a simple Bitmap over a set of positions, sufficient for the
// query layer and tests (spec 07 §11). The storage layer supplies roaring-style
// bitmaps later; the SPI only needs Contains and Count.
type SliceBitmap struct {
	set map[uint32]struct{}
}

// NewSliceBitmap builds a bitmap from the given positions.
func NewSliceBitmap(positions []uint32) *SliceBitmap {
	m := make(map[uint32]struct{}, len(positions))
	for _, p := range positions {
		m[p] = struct{}{}
	}
	return &SliceBitmap{set: m}
}

func (b *SliceBitmap) Contains(pos uint32) bool {
	_, ok := b.set[pos]
	return ok
}

func (b *SliceBitmap) Count() int { return len(b.set) }
