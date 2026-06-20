package storage

import "sync"

// colSegment is one column's slice of values for one vector-segment seq
// (spec 04 §5.2). Parallel to the vector segment (invariant I-7): slot p here is
// the same point as slot p in the vector segment of the same seqNum. Values live
// in a column-major slice so a filter scan reads one column without touching the
// vector bytes (spec 04 §5.1). The zone map is maintained on write (spec 04 §5.8).
type colSegment struct {
	segSeq   uint32
	capacity uint32
	vals     []Value // len grows to nextPos; index is slotPos
	zone     ZoneMap
}

func (cs *colSegment) writeAt(slotPos uint32, v Value) {
	for uint32(len(cs.vals)) <= slotPos {
		cs.vals = append(cs.vals, NullValue)
	}
	cs.vals[slotPos] = v.clone()
	cs.zone.fold(slotPos, v)
}

func (cs *colSegment) readAt(slotPos uint32) Value {
	if slotPos >= uint32(len(cs.vals)) {
		return NullValue
	}
	return cs.vals[slotPos]
}

// column is the columnar store for one metadata column across all segments
// (spec 04 §5.2). Each (colID, segSeq) is a colSegment.
type column struct {
	def  ColumnDef
	segs map[uint32]*colSegment
}

func (c *column) segment(segSeq, capacity uint32) *colSegment {
	cs := c.segs[segSeq]
	if cs == nil {
		cs = &colSegment{segSeq: segSeq, capacity: capacity}
		c.segs[segSeq] = cs
	}
	return cs
}

// metadataStore is the per-collection columnar metadata store (spec 04 §5,
// §15.4). It is owned by one collection and guarded by the engine's collection
// lock; the internal mutex guards zone-map rebuilds that may run concurrently
// with reads (spec 04 §13).
type metadataStore struct {
	mu   sync.RWMutex
	cols map[ColID]*column
}

func newMetadataStore(defs []ColumnDef) *metadataStore {
	m := &metadataStore{cols: make(map[ColID]*column, len(defs))}
	for _, d := range defs {
		m.cols[d.ID] = &column{def: d, segs: make(map[uint32]*colSegment)}
	}
	return m
}

func (m *metadataStore) has(colID ColID) bool {
	_, ok := m.cols[colID]
	return ok
}

// writeRow writes a whole MetaRow at (segSeq, slotPos) (spec 04 §8.1). Columns not
// present in the row are written NULL. Unknown column ids are rejected upstream.
func (m *metadataStore) writeRow(segSeq, slotPos, capacity uint32, row MetaRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, col := range m.cols {
		v, ok := row[id]
		if !ok {
			v = NullValue
		}
		if !v.IsNull() && !valueMatchesType(v, col.def.Type) {
			return ErrSchemaMismatch
		}
		col.segment(segSeq, capacity).writeAt(slotPos, v)
	}
	for id := range row {
		if _, ok := m.cols[id]; !ok {
			return ErrUnknownColumn
		}
	}
	return nil
}

// ReadAt returns the value of colID at (segSeq, slotPos) (spec 04 §15.4).
func (m *metadataStore) ReadAt(colID ColID, segSeq, slotPos uint32, snap Snapshot) (Value, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	col := m.cols[colID]
	if col == nil {
		return NullValue, ErrUnknownColumn
	}
	cs := col.segs[segSeq]
	if cs == nil {
		return NullValue, nil
	}
	return cs.readAt(slotPos), nil
}

// WriteAt writes one cell (spec 04 §8.7 in-place fixed-width overwrite).
func (m *metadataStore) WriteAt(colID ColID, segSeq, slotPos, capacity uint32, val Value) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	col := m.cols[colID]
	if col == nil {
		return ErrUnknownColumn
	}
	if !val.IsNull() && !valueMatchesType(val, col.def.Type) {
		return ErrSchemaMismatch
	}
	col.segment(segSeq, capacity).writeAt(slotPos, val)
	return nil
}

// ZoneMap returns the zone map for one column segment (spec 04 §15.4).
func (m *metadataStore) ZoneMap(colID ColID, segSeq uint32) (*ZoneMap, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	col := m.cols[colID]
	if col == nil {
		return nil, ErrUnknownColumn
	}
	cs := col.segs[segSeq]
	if cs == nil {
		return &ZoneMap{}, nil
	}
	return &cs.zone, nil
}

// RebuildZoneMaps recomputes every zone map for colID from the stored values
// (spec 04 §15.4, §5.8 compaction-time rebuild).
func (m *metadataStore) RebuildZoneMaps(colID ColID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	col := m.cols[colID]
	if col == nil {
		return ErrUnknownColumn
	}
	for _, cs := range col.segs {
		cs.zone = ZoneMap{}
		for p, v := range cs.vals {
			cs.zone.fold(uint32(p), v)
		}
	}
	return nil
}

// zoneMapsFor returns the zone maps of every column for a given seg, keyed by
// column id, used by the filter scan to skip blocks (spec 04 §10.5).
func (m *metadataStore) zoneMapsFor(segSeq uint32) map[ColID]*ZoneMap {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[ColID]*ZoneMap, len(m.cols))
	for id, col := range m.cols {
		if cs := col.segs[segSeq]; cs != nil {
			out[id] = &cs.zone
		}
	}
	return out
}

// dropSegment removes all column data for a sealed seq after compaction frees it
// (spec 04 §9.8).
func (m *metadataStore) dropSegment(segSeq uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, col := range m.cols {
		delete(col.segs, segSeq)
	}
}

// valueMatchesType reports whether a value's kind is compatible with a column type
// (spec 04 §15.5 schema mismatch). Timestamp accepts int kinds; the rest are exact.
func valueMatchesType(v Value, t ColType) bool {
	switch t {
	case ColInt64:
		return v.Kind == KindInt
	case ColFloat64:
		return v.Kind == KindFloat || v.Kind == KindInt
	case ColBool:
		return v.Kind == KindBool
	case ColTimestamp:
		return v.Kind == KindTimestamp || v.Kind == KindInt
	case ColText:
		return v.Kind == KindText
	case ColBytes:
		return v.Kind == KindBytes
	default:
		return false
	}
}
