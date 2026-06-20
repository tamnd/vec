package storage

import (
	"sort"
	"sync"

	"github.com/tamnd/vector/mvcc"
)

// Engine is the storage engine for one database (spec 04 §15.1). It owns the
// collections, the MVCC clock, and the single-writer lock. Reads take the read
// lock and resolve through MVCC snapshots; writes serialize on the writer lock and
// publish atomically at commit (spec 04 §13.1).
type Engine struct {
	mu       sync.RWMutex // guards collection structures
	writerMu sync.Mutex   // serializes write transactions (single-writer)

	clock  *mvcc.Clock
	oracle *mvcc.WatermarkOracle

	colls      map[uint64]*collection
	compacting map[uint64]bool

	// repointHook is called after a compaction with the full repoint table so the
	// Index SPI can renumber its positions (spec 04 §9.5, §15.6). The db layer wires
	// it to index.RenumberPositions; nil when no index is attached.
	repointHook func(collID uint64, rp []Repoint) error
}

// NewEngine creates an empty engine (spec 04 §15.1).
func NewEngine() *Engine {
	clk := mvcc.NewClock(1)
	return &Engine{
		clock:      clk,
		oracle:     mvcc.NewWatermarkOracle(clk),
		colls:      make(map[uint64]*collection),
		compacting: make(map[uint64]bool),
	}
}

// SetRepointHook registers the compaction repoint callback (spec 04 §9.5).
func (e *Engine) SetRepointHook(h func(collID uint64, rp []Repoint) error) {
	e.mu.Lock()
	e.repointHook = h
	e.mu.Unlock()
}

// CreateCollection registers a new collection (spec 04 §22.1). It is a DDL
// operation, not part of a data transaction in this build.
func (e *Engine) CreateCollection(def CollectionDef) error {
	if def.Dims == 0 {
		return ErrDimensionMismatch
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.colls[def.ID]; ok {
		return ErrDuplicateID
	}
	e.colls[def.ID] = newCollection(def)
	return nil
}

// Snapshot returns a read snapshot at the current commit point (spec 06 §2.1).
func (e *Engine) Snapshot() Snapshot {
	return &mvcc.Snapshot{ReadSeq: e.clock.Current()}
}

func (e *Engine) coll(collID uint64) (*collection, error) {
	c, ok := e.colls[collID]
	if !ok {
		return nil, ErrUnknownCollection
	}
	return c, nil
}

// --- Writes ---

// Insert inserts a new point and returns its collection-level position
// (spec 04 §8.1). Returns ErrDuplicateID if the id is already live.
func (e *Engine) Insert(txn Txn, collID uint64, id PointID, vec []float32, meta MetaRow) (uint32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.coll(collID)
	if err != nil {
		return 0, err
	}
	return e.appendPoint(txn, c, id, vec, meta)
}

// InsertBatch inserts many points as one transaction (spec 04 §8.3).
func (e *Engine) InsertBatch(txn Txn, collID uint64, points []Point) ([]uint32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.coll(collID)
	if err != nil {
		return nil, err
	}
	out := make([]uint32, 0, len(points))
	for _, p := range points {
		pos, err := e.appendPoint(txn, c, p.ID, p.Vec, p.Meta)
		if err != nil {
			return out, err
		}
		out = append(out, pos)
	}
	return out, nil
}

// appendPoint is the core insert (spec 04 §8.1). The caller holds e.mu and the
// transaction holds the writer lock. The slot is stamped at version 0 and
// back-patched to the commit sequence on commit (spec 04 §15.3).
func (e *Engine) appendPoint(txn Txn, c *collection, id PointID, vec []float32, meta MetaRow) (uint32, error) {
	if uint32(len(vec)) != c.dims {
		return 0, ErrDimensionMismatch
	}
	if _, _, err := c.idmap.ForwardLookup(id); err == nil {
		return 0, ErrDuplicateID
	}
	seg := c.ensureOpen()
	dirIdx := c.openIdx
	slotPos := seg.append(id, vec, 0)
	if err := c.meta.writeRow(seg.seqNum, slotPos, c.capacity, meta); err != nil {
		// Roll the just-appended slot back before returning the schema error.
		seg.nextPos--
		seg.liveCount--
		return 0, err
	}
	if err := c.idmap.insert(id, seg.seqNum, slotPos); err != nil {
		seg.nextPos--
		seg.liveCount--
		return 0, err
	}
	txn.onCommit(func(seq mvcc.CommitSeq) { seg.version[slotPos] = seq })
	txn.onAbort(func() {
		c.idmap.tombstone(id)
		delete(c.idmap.dead, id)
		seg.tomb[slotPos>>6] &^= 1 << (slotPos & 63)
		if seg.nextPos == slotPos+1 {
			seg.nextPos--
		}
		if seg.liveCount > 0 {
			seg.liveCount--
		}
	})
	c.writesSinceAnalyze++
	return c.colPos(dirIdx, slotPos), nil
}

// Delete tombstones the point with the given id (spec 04 §8.4).
func (e *Engine) Delete(txn Txn, collID uint64, id PointID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.coll(collID)
	if err != nil {
		return err
	}
	segSeq, slotPos, err := c.idmap.ForwardLookup(id)
	if err != nil {
		return ErrNotFound
	}
	di, ok := c.segBySeq(segSeq)
	if !ok {
		return ErrNotFound
	}
	seg := c.segs[di]
	seg.tombstone(slotPos)
	if err := c.idmap.tombstone(id); err != nil {
		return err
	}
	txn.onAbort(func() {
		seg.tomb[slotPos>>6] &^= 1 << (slotPos & 63)
		seg.liveCount++
		if seg.deadCount > 0 {
			seg.deadCount--
		}
		c.idmap.reassign(id, segSeq, slotPos)
	})
	c.writesSinceAnalyze++
	return nil
}

// Upsert inserts or replaces the point (spec 04 §8.5). Replacing tombstones the
// old slot and appends a new one; the returned bool reports whether the id was new.
func (e *Engine) Upsert(txn Txn, collID uint64, id PointID, vec []float32, meta MetaRow) (uint32, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.coll(collID)
	if err != nil {
		return 0, false, err
	}
	if segSeq, slotPos, err := c.idmap.ForwardLookup(id); err == nil {
		// Existing point: tombstone the old slot, then append a fresh one.
		di, _ := c.segBySeq(segSeq)
		old := c.segs[di]
		old.tombstone(slotPos)
		c.idmap.tombstone(id)
		txn.onAbort(func() {
			old.tomb[slotPos>>6] &^= 1 << (slotPos & 63)
			old.liveCount++
			if old.deadCount > 0 {
				old.deadCount--
			}
		})
		pos, err := e.appendPoint(txn, c, id, vec, meta)
		return pos, false, err
	}
	pos, err := e.appendPoint(txn, c, id, vec, meta)
	return pos, true, err
}

// UpdateMeta updates only the metadata of an existing point (spec 04 §8.7).
func (e *Engine) UpdateMeta(txn Txn, collID uint64, id PointID, meta MetaRow) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.coll(collID)
	if err != nil {
		return err
	}
	segSeq, slotPos, err := c.idmap.ForwardLookup(id)
	if err != nil {
		return ErrNotFound
	}
	for colID, v := range meta {
		if !c.meta.has(colID) {
			return ErrUnknownColumn
		}
		prev, _ := c.meta.ReadAt(colID, segSeq, slotPos, nil)
		cID, sSeq, sPos, nv := colID, segSeq, slotPos, v
		if err := c.meta.WriteAt(cID, sSeq, sPos, c.capacity, nv); err != nil {
			return err
		}
		txn.onAbort(func() { _ = c.meta.WriteAt(cID, sSeq, sPos, c.capacity, prev) })
	}
	c.writesSinceAnalyze++
	return nil
}

// --- Reads ---

// Fetch resolves a position to a full point record (spec 04 §7.1). proj selects
// metadata columns; nil means all.
func (e *Engine) Fetch(collID uint64, P uint32, proj []ColID, snap Snapshot) (PointRecord, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return PointRecord{}, err
	}
	return e.fetchLocked(c, P, proj, snap)
}

func (e *Engine) fetchLocked(c *collection, P uint32, proj []ColID, snap Snapshot) (PointRecord, error) {
	di, slotPos, ok := c.locate(P)
	if !ok {
		return PointRecord{}, ErrPositionOutOfRange
	}
	seg := c.segs[di]
	if slotPos >= seg.nextPos {
		return PointRecord{}, ErrPositionOutOfRange
	}
	if seg.IsTombstoned(slotPos) {
		return PointRecord{}, ErrDeleted
	}
	if snap != nil && !snap.IsVisible(seg.version[slotPos], 0) {
		return PointRecord{}, ErrNotVisible
	}
	id, _ := seg.BackwardMapLookup(slotPos)
	vec := make([]float32, c.dims)
	_ = seg.FetchVector(slotPos, vec)
	row := e.projectMeta(c, seg.seqNum, slotPos, proj, snap)
	return PointRecord{Pos: P, ID: id, Vec: vec, Meta: row}, nil
}

func (e *Engine) projectMeta(c *collection, segSeq, slotPos uint32, proj []ColID, snap Snapshot) MetaRow {
	cols := proj
	if cols == nil {
		cols = make([]ColID, 0, len(c.columns))
		for _, cd := range c.columns {
			cols = append(cols, cd.ID)
		}
	}
	row := make(MetaRow, len(cols))
	for _, cid := range cols {
		v, err := c.meta.ReadAt(cid, segSeq, slotPos, snap)
		if err == nil && !v.IsNull() {
			row[cid] = v
		}
	}
	return row
}

// FetchBatch resolves many positions (spec 04 §7.5). Positions that are out of
// range, tombstoned, or not visible are skipped; the result holds the resolvable
// records in the input order.
func (e *Engine) FetchBatch(collID uint64, positions []uint32, proj []ColID, snap Snapshot) ([]PointRecord, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return nil, err
	}
	out := make([]PointRecord, 0, len(positions))
	for _, p := range positions {
		rec, err := e.fetchLocked(c, p, proj, snap)
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// FetchVector fills buf with the full-precision vector at position P (spec 04
// §15.1). Used by the Index SPI for reranking; buf must hold dims floats.
func (e *Engine) FetchVector(collID uint64, P uint32, buf []float32) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return err
	}
	di, slotPos, ok := c.locate(P)
	if !ok {
		return ErrPositionOutOfRange
	}
	seg := c.segs[di]
	if seg.IsTombstoned(slotPos) {
		return ErrDeleted
	}
	if uint32(len(buf)) != c.dims {
		return ErrDimensionMismatch
	}
	return seg.FetchVector(slotPos, buf)
}

// ScanVectors calls cb for every live, snapshot-visible position in ascending
// position order (spec 04 §10.2). The flat index and IVF training use this.
func (e *Engine) ScanVectors(collID uint64, snap Snapshot, cb func(pos uint32, vec []float32) bool) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return err
	}
	for di, seg := range c.segs {
		stop := false
		for slotPos := uint32(0); slotPos < seg.nextPos; slotPos++ {
			if !seg.visibleLive(slotPos, snap) {
				continue
			}
			buf := make([]float32, c.dims)
			_ = seg.FetchVector(slotPos, buf)
			if !cb(c.colPos(di, slotPos), buf) {
				stop = true
				break
			}
		}
		if stop {
			break
		}
	}
	return nil
}

// --- Id-map ---

// LookupID resolves a point id to its collection-level position (spec 04 §15.1).
func (e *Engine) LookupID(collID uint64, id PointID) (uint32, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return 0, err
	}
	segSeq, slotPos, err := c.idmap.ForwardLookup(id)
	if err != nil {
		return 0, ErrNotFound
	}
	di, ok := c.segBySeq(segSeq)
	if !ok {
		return 0, ErrNotFound
	}
	return c.colPos(di, slotPos), nil
}

// LookupPos resolves a position to the point id at that position (spec 04 §15.1).
func (e *Engine) LookupPos(collID uint64, P uint32) (PointID, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return 0, err
	}
	di, slotPos, ok := c.locate(P)
	if !ok {
		return 0, ErrPositionOutOfRange
	}
	seg := c.segs[di]
	if slotPos >= seg.nextPos {
		return 0, ErrPositionOutOfRange
	}
	if seg.IsTombstoned(slotPos) {
		return 0, ErrDeleted
	}
	return seg.BackwardMapLookup(slotPos)
}

// --- Filter support ---

// MetadataFilter evaluates pred against all live, snapshot-visible positions and
// returns a bitmap of the matches (spec 04 §10.5). Zone maps skip whole blocks the
// predicate provably rejects (spec 04 §5.4).
func (e *Engine) MetadataFilter(collID uint64, pred Predicate, snap Snapshot) (*PositionBitmap, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return nil, err
	}
	bm := NewPositionBitmap(uint32(len(c.segs)) * c.capacity)
	for di, seg := range c.segs {
		zones := c.meta.zoneMapsFor(seg.seqNum)
		nblocks := int(seg.nextPos+zoneBlockSize-1) / zoneBlockSize
		for b := 0; b < nblocks; b++ {
			if blockSkippable(pred, zones, b) {
				continue
			}
			lo := uint32(b) * zoneBlockSize
			hi := lo + zoneBlockSize
			if hi > seg.nextPos {
				hi = seg.nextPos
			}
			for slotPos := lo; slotPos < hi; slotPos++ {
				if !seg.visibleLive(slotPos, snap) {
					continue
				}
				get := func(cid ColID) Value {
					v, _ := c.meta.ReadAt(cid, seg.seqNum, slotPos, snap)
					return v
				}
				if pred.eval(get) {
					bm.Set(c.colPos(di, slotPos))
				}
			}
		}
	}
	return bm, nil
}

// --- Statistics ---

// CollectionStats returns aggregate statistics for the planner (spec 04 §12.2).
func (e *Engine) CollectionStats(collID uint64) (CollectionStats, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return CollectionStats{}, err
	}
	live, dead := c.liveTotal()
	staleness := 0.0
	if c.writesSinceAnalyze > 0 {
		const autoAnalyzeThreshold = 10000.0
		staleness = float64(c.writesSinceAnalyze) / autoAnalyzeThreshold
		if staleness > 1 {
			staleness = 1
		}
	}
	return CollectionStats{
		TotalPoints:        live + dead,
		LivePoints:         live,
		TombstoneCount:     dead,
		SegmentCount:       len(c.segs),
		Dims:               c.dims,
		Stride:             c.stride,
		StalenessScore:     staleness,
		WritesSinceAnalyze: c.writesSinceAnalyze,
	}, nil
}

// ColumnStats returns per-column statistics, computing them on demand if Analyze
// has not cached them (spec 04 §12.3).
func (e *Engine) ColumnStats(collID uint64, colID ColID) (ColumnStats, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return ColumnStats{}, err
	}
	if cs, ok := c.colStats[colID]; ok {
		return cs, nil
	}
	if !c.meta.has(colID) {
		return ColumnStats{}, ErrUnknownColumn
	}
	return e.computeColumnStats(c, colID), nil
}

// Analyze rebuilds and caches statistics for a collection (spec 04 §12.5).
func (e *Engine) Analyze(collID uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.coll(collID)
	if err != nil {
		return err
	}
	c.colStats = make(map[ColID]ColumnStats, len(c.columns))
	for _, cd := range c.columns {
		c.colStats[cd.ID] = e.computeColumnStats(c, cd.ID)
	}
	c.writesSinceAnalyze = 0
	return nil
}

// computeColumnStats scans one column over the live points to build null fraction,
// distinct count, min/max, and an equi-depth histogram (spec 04 §21.2).
func (e *Engine) computeColumnStats(c *collection, colID ColID) ColumnStats {
	cs := ColumnStats{ColID: colID}
	var nums []float64
	distinct := make(map[string]struct{})
	var total, nulls uint64
	var minV, maxV Value
	haveMinMax := false
	for _, seg := range c.segs {
		for slotPos := uint32(0); slotPos < seg.nextPos; slotPos++ {
			if seg.IsTombstoned(slotPos) {
				continue
			}
			total++
			v, _ := c.meta.ReadAt(colID, seg.seqNum, slotPos, nil)
			if v.IsNull() {
				nulls++
				continue
			}
			distinct[distinctKey(v)] = struct{}{}
			if !haveMinMax {
				minV, maxV, haveMinMax = v, v, true
			} else {
				if v.less(minV) {
					minV = v
				}
				if maxV.less(v) {
					maxV = v
				}
			}
			if f, ok := v.asFloat(); ok {
				nums = append(nums, f)
			}
		}
	}
	if total > 0 {
		cs.NullFraction = float64(nulls) / float64(total)
	}
	cs.DistinctCount = uint64(len(distinct))
	cs.Min, cs.Max = minV, maxV
	cs.Histogram = buildHistogram(nums, 100)
	return cs
}

// distinctKey renders a value for distinct counting.
func distinctKey(v Value) string {
	switch v.Kind {
	case KindText:
		return "t" + v.S
	case KindBytes:
		return "b" + string(v.Bytes)
	case KindBool:
		if v.B {
			return "B1"
		}
		return "B0"
	default:
		f, _ := v.asFloat()
		return "n" + formatFloat(f)
	}
}

// buildHistogram makes an equi-depth histogram from numeric samples (spec 04 §21.2).
func buildHistogram(nums []float64, buckets int) []HistogramBucket {
	if len(nums) == 0 || buckets <= 0 {
		return nil
	}
	sort.Float64s(nums)
	if buckets > len(nums) {
		buckets = len(nums)
	}
	per := len(nums) / buckets
	if per == 0 {
		per = 1
	}
	out := make([]HistogramBucket, 0, buckets)
	for i := 0; i < len(nums); i += per {
		hi := i + per
		if hi > len(nums) {
			hi = len(nums)
		}
		out = append(out, HistogramBucket{
			Lo:    nums[i],
			Hi:    nums[hi-1],
			Count: uint64(hi - i),
		})
	}
	return out
}

// --- Lifecycle ---

// SealSegment manually seals the open segment (spec 04 §15.1).
func (e *Engine) SealSegment(collID uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, err := e.coll(collID)
	if err != nil {
		return err
	}
	if c.openIdx >= 0 {
		c.segs[c.openIdx].sealed = true
		c.openIdx = -1
	}
	return nil
}

// WarmCache is a no-op in the in-memory build: all segments are resident
// (spec 04 §15.1). The pager-backed build prefetches vector pages here.
func (e *Engine) WarmCache(collID uint64) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, err := e.coll(collID)
	return err
}

// Close releases engine resources (spec 04 §15.1).
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.colls = make(map[uint64]*collection)
	return nil
}

// VectorSegments returns the collection's segments as the VectorSegment interface
// (spec 04 §15.2), for the index build loop and diagnostics.
func (e *Engine) VectorSegments(collID uint64) ([]VectorSegment, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return nil, err
	}
	out := make([]VectorSegment, len(c.segs))
	for i, s := range c.segs {
		out[i] = s
	}
	return out, nil
}

// IdMapStats returns id-map statistics for a collection (spec 04 §15.3).
func (e *Engine) IdMapStats(collID uint64) (IdMapStats, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, err := e.coll(collID)
	if err != nil {
		return IdMapStats{}, err
	}
	return c.idmap.stats(), nil
}
