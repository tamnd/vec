package storage

import "github.com/tamnd/vec/mvcc"

// defaultTombstoneRatio is the compaction trigger fraction (spec 04 §9.3): when a
// collection's tombstones exceed this share of total points, compaction reclaims
// the dead slots.
const defaultTombstoneRatio = 0.20

// liveRef is one live point gathered for compaction, in ascending old-position
// order (spec 04 §9.4).
type liveRef struct {
	oldPos  uint32
	segSeq  uint32
	slotPos uint32
	id      PointID
	version mvcc.CommitSeq
}

// Compact rewrites the collection's live points into a fresh, gap-free segment
// directory and reclaims tombstoned slots (spec 04 §9.4). It always performs a
// full-collection compaction in this build, so it produces a complete repoint
// table covering every live point; the lo/hi range is advisory. The repoint table
// is handed to the Index SPI through the registered hook so indexes renumber their
// positions in lockstep (spec 04 §9.5, §15.6).
func (e *Engine) Compact(collID uint64, lo, hi uint32) error {
	// Hold the writer lock so no insert/delete races the directory swap (spec 04
	// §13.4, §16.6).
	e.writerMu.Lock()
	defer e.writerMu.Unlock()
	e.mu.Lock()
	if e.compacting[collID] {
		e.mu.Unlock()
		return ErrCompactionActive
	}
	c, err := e.coll(collID)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	e.compacting[collID] = true
	defer func() {
		e.mu.Lock()
		delete(e.compacting, collID)
		e.mu.Unlock()
	}()

	// Phase 1: gather live points in ascending old-position order (spec 04 §16.6).
	var live []liveRef
	for di, seg := range c.segs {
		for slotPos := uint32(0); slotPos < seg.nextPos; slotPos++ {
			if seg.IsTombstoned(slotPos) {
				continue
			}
			id, _ := seg.BackwardMapLookup(slotPos)
			live = append(live, liveRef{
				oldPos:  c.colPos(di, slotPos),
				segSeq:  seg.seqNum,
				slotPos: slotPos,
				id:      id,
				version: seg.version[slotPos],
			})
		}
	}

	// Phase 2: write live points into new segments, copying the single vector copy
	// and the metadata columns, building the repoint table (spec 04 §16.6).
	newSegs := make([]*segment, 0, (len(live)/int(c.capacity))+1)
	newMeta := newMetadataStore(c.columns)
	repoint := make([]Repoint, 0, len(live))
	var cur *segment
	curDir := -1
	buf := make([]float32, c.dims)
	startSeq := c.nextSeq
	for _, lr := range live {
		if cur == nil || cur.full() {
			if cur != nil {
				cur.sealed = true
			}
			cur = newSegment(startSeq, c.elem, c.dims, c.stride, c.capacity, c.codec)
			startSeq++
			newSegs = append(newSegs, cur)
			curDir = len(newSegs) - 1
		}
		// Copy the vector through the codec (single-copy invariant preserved).
		di, _ := c.segBySeq(lr.segSeq)
		_ = c.segs[di].FetchVector(lr.slotPos, buf)
		newSlot := cur.append(lr.id, buf, lr.version)
		newPos := uint32(curDir)*c.capacity + newSlot
		// Copy metadata columns.
		for _, cd := range c.columns {
			v, _ := c.meta.ReadAt(cd.ID, lr.segSeq, lr.slotPos, nil)
			_ = newMeta.WriteAt(cd.ID, cur.seqNum, newSlot, c.capacity, v)
		}
		repoint = append(repoint, Repoint{OldPos: lr.oldPos, NewPos: newPos, PointID: lr.id})
	}

	// Phase 3: swap the directory and rebuild the id-map forward entries (spec 04
	// §16.6 write-lock window). newPos -> (segSeq, slotPos) for the repoint apply.
	newLoc := func(newPos uint32) idLoc {
		di := int(newPos / c.capacity)
		return idLoc{segSeq: newSegs[di].seqNum, slotPos: newPos % c.capacity}
	}
	c.idmap.applyRepoint(repoint, newLoc)
	c.segs = newSegs
	c.meta = newMeta
	c.nextSeq = startSeq
	c.rebuildDir()

	// Phase 4: hand the repoint table to the Index SPI so it renumbers in lockstep
	// (spec 04 §9.5). Done outside the structural mutation but under e.mu.
	hook := e.repointHook
	e.mu.Unlock()
	if hook != nil {
		if err := hook(collID, repoint); err != nil {
			return err
		}
	}
	return nil
}

// ShouldCompact reports whether a collection's tombstone fraction has crossed the
// compaction threshold (spec 04 §9.3).
func (e *Engine) ShouldCompact(collID uint64) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return false
	}
	live, dead := c.liveTotal()
	total := live + dead
	if total == 0 {
		return false
	}
	return float64(dead)/float64(total) >= defaultTombstoneRatio
}
