package storage

import "github.com/tamnd/vector/distance"

// collection is the in-memory state of one collection (spec 04 §22, §23). It owns
// an ordered directory of vector segments, the parallel metadata column store, and
// the id-map. Positions are collection-scoped and dense within the capacity grid:
// collection-level position = dirIndex*capacity + slotPos (spec 04 §3.9, §16.7),
// satisfying directory contiguity (invariant I-6).
type collection struct {
	id        uint64
	name      string
	dims      uint32
	elem      ElemType
	stride    uint32
	metric    distance.Metric
	codec     vecCodec
	int8Scale float32
	capacity  uint32 // per-segment slot capacity (uniform across the directory)

	columns []ColumnDef
	segs    []*segment
	dirOf   map[uint32]int // segSeq -> directory index
	openIdx int            // directory index of the open segment, -1 if none
	nextSeq uint32         // next segment seq number to allocate

	idmap *idMap
	meta  *metadataStore

	writesSinceAnalyze uint64
	colStats           map[ColID]ColumnStats // cached by Analyze
}

// segCapacity derives the per-segment point capacity from the def (spec 04 §3.5):
// the 256 MB segment target divided by the stride, floored at 1.
func segCapacity(def CollectionDef, stride uint32) uint32 {
	if def.SegmentCapacity > 0 {
		return def.SegmentCapacity
	}
	const segmentTargetBytes = 256 << 20
	cap := uint32(segmentTargetBytes / int(stride))
	if cap == 0 {
		cap = 1
	}
	return cap
}

// newCollection builds the in-memory state for a freshly created collection.
func newCollection(def CollectionDef) *collection {
	stride := computeStride(def.Elem, def.Dims)
	cols := make([]ColumnDef, len(def.Columns))
	copy(cols, def.Columns)
	return &collection{
		id:        def.ID,
		name:      def.Name,
		dims:      def.Dims,
		elem:      def.Elem,
		stride:    stride,
		metric:    def.Metric,
		codec:     codecFor(def.Elem, def.Dims, def.Int8Scale),
		int8Scale: def.Int8Scale,
		capacity:  segCapacity(def, stride),
		columns:   cols,
		dirOf:     make(map[uint32]int),
		openIdx:   -1,
		idmap:     newIDMap(),
		meta:      newMetadataStore(cols),
	}
}

// colPos maps a directory index and slot to a collection-level position.
func (c *collection) colPos(dirIdx int, slotPos uint32) uint32 {
	return uint32(dirIdx)*c.capacity + slotPos
}

// locate maps a collection-level position to its directory index and slot
// (spec 04 §3.9). ok is false when the position lies outside the directory grid.
func (c *collection) locate(p uint32) (dirIdx int, slotPos uint32, ok bool) {
	di := int(p / c.capacity)
	if di >= len(c.segs) {
		return 0, 0, false
	}
	return di, p % c.capacity, true
}

// segBySeq returns the directory index for a segment seq number.
func (c *collection) segBySeq(segSeq uint32) (int, bool) {
	di, ok := c.dirOf[segSeq]
	return di, ok
}

// ensureOpen returns the open segment, sealing a full one and creating a fresh
// segment as needed (spec 04 §9.1, §9.2). The caller holds the engine write path.
func (c *collection) ensureOpen() *segment {
	if c.openIdx >= 0 && !c.segs[c.openIdx].full() {
		return c.segs[c.openIdx]
	}
	if c.openIdx >= 0 {
		c.segs[c.openIdx].sealed = true // seal the full segment (spec 04 §9.1)
	}
	seg := newSegment(c.nextSeq, c.elem, c.dims, c.stride, c.capacity, c.codec)
	c.nextSeq++
	c.segs = append(c.segs, seg)
	c.dirOf[seg.seqNum] = len(c.segs) - 1
	c.openIdx = len(c.segs) - 1
	return seg
}

// rebuildDir recomputes dirOf and openIdx after the directory is replaced
// (spec 04 §9.8). The last segment is the open one unless every segment is sealed.
func (c *collection) rebuildDir() {
	c.dirOf = make(map[uint32]int, len(c.segs))
	c.openIdx = -1
	for i, s := range c.segs {
		c.dirOf[s.seqNum] = i
		if !s.sealed {
			c.openIdx = i
		}
	}
}

// liveTotal returns the live and tombstone counts across the directory.
func (c *collection) liveTotal() (live, dead uint64) {
	for _, s := range c.segs {
		live += uint64(s.liveCount)
		dead += uint64(s.deadCount)
	}
	return live, dead
}
