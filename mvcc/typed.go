package mvcc

// This file gives the three concrete version chains vec versions: vectors,
// metadata columns, and the id-map (spec 06 §2.2-2.4). Each is a thin typed
// facade over the generic VersionTable so the db and storage layers read in
// domain terms (position, point id) rather than generic keys. The spec-named
// delta entry structs are kept as the canonical record shape the WAL serializes
// and the recovery path reconstructs.

// PointID is the stable external identifier of a point. It never repeats within a
// collection's lifetime, even after the point is deleted and its position
// reclaimed (spec 06 §2.4). vec stores it as a u64 here; the variable-length byte
// form noted in spec 06 §2.4 is encoded to a u64 surrogate by the id-map at the
// storage layer.
type PointID uint64

// Position is the dense internal index of a point within a collection's vector
// segment. Positions are reused after GC (spec 06 §2.7); a tombstoned position
// returns to the freelist once no live snapshot can see it.
type Position uint32

// Unassigned marks an id-map entry with no position (a tombstone target,
// spec 06 §2.4).
const Unassigned = ^uint32(0)

// VectorDeltaEntry is the canonical vector version record (spec 06 §2.2). The
// in-memory chain stores its payload; this struct is the shape the WAL frame
// carries and the recovery driver replays.
type VectorDeltaEntry struct {
	Position  uint32
	CommitSeq CommitSeq
	TxnID     TxnID
	Data      []float32 // new raw vector, or nil for a deletion
	Tombstone bool
}

// MetaDeltaEntry is the canonical metadata-column version record (spec 06 §2.3).
// Value is the typed scalar (TEXT/INTEGER/FLOAT/BOOLEAN/BLOB/TIMESTAMP) the data
// model defines; mvcc treats it opaquely so the layer stays decoupled from the
// catalog.
type MetaDeltaEntry struct {
	ColumnID  uint16
	Position  uint32
	CommitSeq CommitSeq
	TxnID     TxnID
	Value     any // nil = NULL
	Tombstone bool
}

// IDMapDeltaEntry is the canonical id-map version record (spec 06 §2.4).
type IDMapDeltaEntry struct {
	PointID   PointID
	Position  uint32 // Unassigned for a tombstone
	CommitSeq CommitSeq
	TxnID     TxnID
	Tombstone bool
}

// VectorDelta is the per-collection vector version chain keyed by position.
type VectorDelta struct {
	tbl *VersionTable[uint32, []float32]
}

// NewVectorDelta returns an empty vector delta.
func NewVectorDelta() *VectorDelta { return &VectorDelta{tbl: NewVersionTable[uint32, []float32]()} }

// Put stages a new vector value at pos for txn (spec 06 §2.2).
func (d *VectorDelta) Put(pos uint32, txn TxnID, data []float32) { d.tbl.Stage(pos, txn, data, false) }

// Delete stages a tombstone at pos for txn (spec 06 §2.4).
func (d *VectorDelta) Delete(pos uint32, txn TxnID) { d.tbl.Stage(pos, txn, nil, true) }

// Visible returns the vector visible to s at pos, or ok=false to read the base
// segment value (or when the point is tombstoned for this snapshot).
func (d *VectorDelta) Visible(pos uint32, s *Snapshot) (data []float32, ok bool) {
	return d.tbl.Visible(pos, s)
}

// Commit and Abort publish or discard txn's staged vectors.
func (d *VectorDelta) Commit(txn TxnID, seq CommitSeq) { d.tbl.Commit(txn, seq) }
func (d *VectorDelta) Abort(txn TxnID)                 { d.tbl.Abort(txn) }

// GC folds versions below the watermark into the base.
func (d *VectorDelta) GC(watermark CommitSeq) { d.tbl.GC(watermark) }

// MetaKey addresses one metadata cell: a column within a position.
type MetaKey struct {
	ColumnID uint16
	Position uint32
}

// MetaDelta is the per-collection metadata version chain keyed by (column,
// position).
type MetaDelta struct {
	tbl *VersionTable[MetaKey, any]
}

// NewMetaDelta returns an empty metadata delta.
func NewMetaDelta() *MetaDelta { return &MetaDelta{tbl: NewVersionTable[MetaKey, any]()} }

// Put stages a new column value; Delete stages a row tombstone for the cell.
func (d *MetaDelta) Put(col uint16, pos uint32, txn TxnID, val any) {
	d.tbl.Stage(MetaKey{col, pos}, txn, val, false)
}
func (d *MetaDelta) Delete(col uint16, pos uint32, txn TxnID) {
	d.tbl.Stage(MetaKey{col, pos}, txn, nil, true)
}

// Visible returns the column value visible to s, or found=false to read the base.
func (d *MetaDelta) Visible(col uint16, pos uint32, s *Snapshot) (val any, found bool) {
	v, tomb, found := d.tbl.VisibleEntry(MetaKey{col, pos}, s)
	if !found || tomb {
		return nil, false
	}
	return v, true
}

func (d *MetaDelta) Commit(txn TxnID, seq CommitSeq) { d.tbl.Commit(txn, seq) }
func (d *MetaDelta) Abort(txn TxnID)                 { d.tbl.Abort(txn) }
func (d *MetaDelta) GC(watermark CommitSeq)          { d.tbl.GC(watermark) }

// IDMap is the versioned bijection point_id <-> position (spec 06 §2.4). The
// forward chain maps a point id to its position; the reverse chain maps a
// position back to its point id for kNN result translation. Both are versioned
// with one CommitSeq discipline so the two sides move together.
type IDMap struct {
	forward *VersionTable[PointID, uint32]
	reverse *VersionTable[uint32, PointID]
}

// NewIDMap returns an empty id-map.
func NewIDMap() *IDMap {
	return &IDMap{
		forward: NewVersionTable[PointID, uint32](),
		reverse: NewVersionTable[uint32, PointID](),
	}
}

// Assign stages the bijection id<->pos for txn (an insert or upsert, spec 06 §5.3).
func (m *IDMap) Assign(id PointID, pos uint32, txn TxnID) {
	m.forward.Stage(id, txn, pos, false)
	m.reverse.Stage(pos, txn, id, false)
}

// Delete tombstones both sides for txn (spec 06 §2.4); the position may be
// reclaimed after GC.
func (m *IDMap) Delete(id PointID, pos uint32, txn TxnID) {
	m.forward.Stage(id, txn, Unassigned, true)
	m.reverse.Stage(pos, txn, id, true)
}

// Lookup resolves id to its visible position; ok=false when the point is unknown
// or tombstoned for s.
func (m *IDMap) Lookup(id PointID, s *Snapshot) (pos uint32, ok bool) {
	return m.forward.Visible(id, s)
}

// Reverse resolves pos back to its visible point id, used to translate kNN
// results (spec 06 §2.4).
func (m *IDMap) Reverse(pos uint32, s *Snapshot) (id PointID, ok bool) {
	return m.reverse.Visible(pos, s)
}

func (m *IDMap) Commit(txn TxnID, seq CommitSeq) {
	m.forward.Commit(txn, seq)
	m.reverse.Commit(txn, seq)
}

func (m *IDMap) Abort(txn TxnID) {
	m.forward.Abort(txn)
	m.reverse.Abort(txn)
}

func (m *IDMap) GC(watermark CommitSeq) {
	m.forward.GC(watermark)
	m.reverse.GC(watermark)
}
