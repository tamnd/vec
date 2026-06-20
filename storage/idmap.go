package storage

// idLoc is a forward-map target: which segment and slot a point id lives in
// (spec 04 §6.2). The collection maps this to a collection-level position through
// the segment directory.
type idLoc struct {
	segSeq  uint32
	slotPos uint32
}

// idMap is the per-collection id-map (spec 04 §6). The forward map (point id ->
// location) is the on-disk B-tree's in-memory cache (spec 04 §6.8); the backward
// map (location -> point id) lives in each vector segment's backward array
// (spec 04 §6.3), so this type only owns the forward direction plus the tombstone
// set. Invariants I-2 and I-3 (spec 04 §25.1) tie the two directions together.
type idMap struct {
	fwd  map[PointID]idLoc
	dead map[PointID]struct{}
}

func newIDMap() *idMap {
	return &idMap{
		fwd:  make(map[PointID]idLoc),
		dead: make(map[PointID]struct{}),
	}
}

// ForwardLookup resolves a point id to its (segSeq, slotPos) (spec 04 §15.3).
// A tombstoned or unknown id returns ErrNotFound.
func (m *idMap) ForwardLookup(id PointID) (segSeq, slotPos uint32, err error) {
	loc, ok := m.fwd[id]
	if !ok {
		return 0, 0, ErrNotFound
	}
	return loc.segSeq, loc.slotPos, nil
}

// insert records a new mapping (spec 04 §15.3). Re-inserting a tombstoned id (an
// upsert of a once-deleted id is disallowed by spec 04 §16.7, but a fresh id that
// happens to equal a tombstoned one is impossible since ids are never reused)
// clears its tombstone defensively.
func (m *idMap) insert(id PointID, segSeq, slotPos uint32) error {
	if _, live := m.fwd[id]; live {
		return ErrDuplicateID
	}
	m.fwd[id] = idLoc{segSeq: segSeq, slotPos: slotPos}
	delete(m.dead, id)
	return nil
}

// tombstone removes the live mapping for id and records the tombstone (spec 04
// §6.4). The point id is never reused (spec 04 §16.7).
func (m *idMap) tombstone(id PointID) error {
	if _, ok := m.fwd[id]; !ok {
		return ErrNotFound
	}
	delete(m.fwd, id)
	m.dead[id] = struct{}{}
	return nil
}

// reassign moves an existing live id to a new location, used by upsert (spec 04
// §8.5) and by compaction repointing at the slot level.
func (m *idMap) reassign(id PointID, segSeq, slotPos uint32) {
	m.fwd[id] = idLoc{segSeq: segSeq, slotPos: slotPos}
	delete(m.dead, id)
}

// applyRepoint bulk-updates the forward map after a compaction rewrites slots
// (spec 04 §15.3, §6.6). Each entry carries the point id so the lookup is O(1).
func (m *idMap) applyRepoint(rp []Repoint, locOf func(newPos uint32) idLoc) {
	for _, r := range rp {
		if _, ok := m.fwd[r.PointID]; !ok {
			continue
		}
		m.fwd[r.PointID] = locOf(r.NewPos)
	}
}

// stats reports id-map size (spec 04 §15.3).
func (m *idMap) stats() IdMapStats {
	return IdMapStats{
		LiveEntries:       int64(len(m.fwd)),
		TombstonedEntries: int64(len(m.dead)),
	}
}
