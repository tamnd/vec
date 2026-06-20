package storage

import (
	"math/rand"
	"testing"

	"github.com/tamnd/vector/distance"
)

const (
	testDims = 16
	tsCol    = ColID(1)
	scoreCol = ColID(2)
	tagCol   = ColID(3)
)

func testDef(id uint64, elem ElemType) CollectionDef {
	return CollectionDef{
		ID:     id,
		Name:   "docs",
		Dims:   testDims,
		Elem:   elem,
		Metric: distance.L2Squared,
		Columns: []ColumnDef{
			{ID: tsCol, Name: "ts", Type: ColTimestamp},
			{ID: scoreCol, Name: "score", Type: ColFloat64},
			{ID: tagCol, Name: "tag", Type: ColText, Nullable: true},
		},
		SegmentCapacity: 64, // small so multi-segment paths exercise
	}
}

func randVec(rng *rand.Rand, d int) []float32 {
	v := make([]float32, d)
	for i := range v {
		v[i] = rng.Float32()
	}
	return v
}

func mustInsert(t *testing.T, e *Engine, collID uint64, id PointID, vec []float32, meta MetaRow) uint32 {
	t.Helper()
	tx := e.Begin(true)
	pos, err := e.Insert(tx, collID, id, vec, meta)
	if err != nil {
		tx.Abort()
		t.Fatalf("insert id=%d: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return pos
}

func TestInsertFetchRoundTrip(t *testing.T) {
	e := NewEngine()
	if err := e.CreateCollection(testDef(1, ElemFP32)); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	want := make(map[PointID][]float32)
	for i := 0; i < 200; i++ {
		v := randVec(rng, testDims)
		id := PointID(i + 1)
		want[id] = v
		mustInsert(t, e, 1, id, v, MetaRow{
			tsCol:    Timestamp(int64(i)),
			scoreCol: Float(float64(i) * 0.5),
			tagCol:   Text("tag"),
		})
	}
	snap := e.Snapshot()
	for id, v := range want {
		pos, err := e.LookupID(1, id)
		if err != nil {
			t.Fatalf("lookup id=%d: %v", id, err)
		}
		rec, err := e.Fetch(1, pos, nil, snap)
		if err != nil {
			t.Fatalf("fetch pos=%d: %v", pos, err)
		}
		if rec.ID != id {
			t.Fatalf("pos %d resolved id %d want %d", pos, rec.ID, id)
		}
		for j := range v {
			if rec.Vec[j] != v[j] {
				t.Fatalf("id=%d dim %d: got %v want %v", id, j, rec.Vec[j], v[j])
			}
		}
		if got := rec.Meta[scoreCol]; !got.equal(Float(float64(id-1) * 0.5)) {
			t.Fatalf("id=%d score meta got %+v", id, got)
		}
	}
}

func TestDuplicateIDRejected(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(2))
	mustInsert(t, e, 1, 7, randVec(rng, testDims), nil)
	tx := e.Begin(true)
	if _, err := e.Insert(tx, 1, 7, randVec(rng, testDims), nil); err != ErrDuplicateID {
		t.Fatalf("want ErrDuplicateID, got %v", err)
	}
	tx.Abort()
}

func TestDeleteTombstones(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(3))
	pos := mustInsert(t, e, 1, 11, randVec(rng, testDims), nil)

	tx := e.Begin(true)
	if err := e.Delete(tx, 1, 11); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	snap := e.Snapshot()
	if _, err := e.Fetch(1, pos, nil, snap); err != ErrDeleted {
		t.Fatalf("want ErrDeleted, got %v", err)
	}
	if _, err := e.LookupID(1, 11); err != ErrNotFound {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestAbortRollsBack(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(4))
	tx := e.Begin(true)
	if _, err := e.Insert(tx, 1, 99, randVec(rng, testDims), nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.LookupID(1, 99); err != ErrNotFound {
		t.Fatalf("aborted insert should not be visible, got %v", err)
	}
	st, _ := e.CollectionStats(1)
	if st.LivePoints != 0 {
		t.Fatalf("live points after abort = %d, want 0", st.LivePoints)
	}
}

func TestScanVectorsLiveOnly(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 150; i++ {
		mustInsert(t, e, 1, PointID(i+1), randVec(rng, testDims), nil)
	}
	// Delete every third point.
	for i := 0; i < 150; i += 3 {
		tx := e.Begin(true)
		_ = e.Delete(tx, 1, PointID(i+1))
		_ = tx.Commit()
	}
	snap := e.Snapshot()
	count := 0
	var positions []uint32
	_ = e.ScanVectors(1, snap, func(pos uint32, vec []float32) bool {
		count++
		positions = append(positions, pos)
		return true
	})
	wantLive := 150 - (150+2)/3
	if count != wantLive {
		t.Fatalf("scan visited %d live points, want %d", count, wantLive)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Fatalf("scan not ascending at %d: %d <= %d", i, positions[i], positions[i-1])
		}
	}
}

func TestMetadataFilterRange(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(6))
	for i := 0; i < 300; i++ {
		mustInsert(t, e, 1, PointID(i+1), randVec(rng, testDims), MetaRow{
			tsCol: Timestamp(int64(i)),
		})
	}
	snap := e.Snapshot()
	// ts > 200 should match points 201..299 (ts 201..299), i.e. 99 of them.
	pred := Compare{Col: tsCol, Op: OpGt, Lit: Timestamp(200)}
	bm, err := e.MetadataFilter(1, pred, snap)
	if err != nil {
		t.Fatal(err)
	}
	if got := bm.Count(); got != 99 {
		t.Fatalf("ts>200 matched %d, want 99", got)
	}
	// Verify each set position truly has ts>200.
	for di := uint32(0); di < bm.Len(); di++ {
		if !bm.Contains(di) {
			continue
		}
		rec, err := e.Fetch(1, di, []ColID{tsCol}, snap)
		if err != nil {
			t.Fatalf("fetch %d: %v", di, err)
		}
		if rec.Meta[tsCol].I <= 200 {
			t.Fatalf("pos %d set but ts=%d", di, rec.Meta[tsCol].I)
		}
	}
}

func TestZoneMapSkipMatchesBrute(t *testing.T) {
	// A monotonically increasing ts means zone maps prune most blocks; the result
	// must still equal a brute-force evaluation (spec 04 §25.1 invariant I-5).
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(7))
	const n = 500
	for i := 0; i < n; i++ {
		mustInsert(t, e, 1, PointID(i+1), randVec(rng, testDims), MetaRow{
			tsCol: Timestamp(int64(i * 10)),
		})
	}
	snap := e.Snapshot()
	pred := And{Terms: []Predicate{
		Compare{Col: tsCol, Op: OpGe, Lit: Timestamp(1000)},
		Compare{Col: tsCol, Op: OpLt, Lit: Timestamp(2000)},
	}}
	bm, _ := e.MetadataFilter(1, pred, snap)
	got := bm.Count()
	// ts in [1000,2000) with ts=i*10 -> i in [100,200) -> 100 points.
	if got != 100 {
		t.Fatalf("zone-mapped filter matched %d, want 100", got)
	}
}

func TestCompactionReclaimsAndRepoints(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(8))
	const n = 256
	ids := make([]PointID, 0, n)
	vecs := make(map[PointID][]float32)
	for i := 0; i < n; i++ {
		id := PointID(i + 1)
		v := randVec(rng, testDims)
		ids = append(ids, id)
		vecs[id] = v
		mustInsert(t, e, 1, id, v, MetaRow{scoreCol: Float(float64(i))})
	}
	// Delete half (every even index).
	deleted := make(map[PointID]bool)
	for i := 0; i < n; i += 2 {
		tx := e.Begin(true)
		_ = e.Delete(tx, 1, ids[i])
		_ = tx.Commit()
		deleted[ids[i]] = true
	}
	if !e.ShouldCompact(1) {
		t.Fatal("expected compaction to be due at 50% tombstones")
	}

	// Capture the repoint table the hook receives.
	var repointed []Repoint
	e.SetRepointHook(func(collID uint64, rp []Repoint) error {
		repointed = append(repointed, rp...)
		return nil
	})
	if err := e.Compact(1, 0, 0); err != nil {
		t.Fatalf("compact: %v", err)
	}

	live := n - len(deleted)
	if len(repointed) != live {
		t.Fatalf("repoint table has %d entries, want %d live points", len(repointed), live)
	}
	// Repoint must be sorted by OldPos ascending (spec 04 §15.6).
	for i := 1; i < len(repointed); i++ {
		if repointed[i].OldPos < repointed[i-1].OldPos {
			t.Fatalf("repoint not sorted by OldPos at %d", i)
		}
	}
	st, _ := e.CollectionStats(1)
	if st.LivePoints != uint64(live) || st.TombstoneCount != 0 {
		t.Fatalf("after compact live=%d dead=%d, want live=%d dead=0", st.LivePoints, st.TombstoneCount, live)
	}
	// Every surviving id must still resolve and its vector survive intact.
	snap := e.Snapshot()
	for _, id := range ids {
		if deleted[id] {
			if _, err := e.LookupID(1, id); err != ErrNotFound {
				t.Fatalf("deleted id %d still resolves after compact", id)
			}
			continue
		}
		pos, err := e.LookupID(1, id)
		if err != nil {
			t.Fatalf("live id %d lost after compact: %v", id, err)
		}
		rec, err := e.Fetch(1, pos, nil, snap)
		if err != nil {
			t.Fatalf("fetch live id %d: %v", id, err)
		}
		for j := range vecs[id] {
			if rec.Vec[j] != vecs[id][j] {
				t.Fatalf("id %d vector dim %d changed by compaction", id, j)
			}
		}
	}
}

func TestUpsertReplaces(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(9))
	v1 := randVec(rng, testDims)
	tx := e.Begin(true)
	_, isNew, err := e.Upsert(tx, 1, 42, v1, MetaRow{scoreCol: Float(1)})
	if err != nil || !isNew {
		t.Fatalf("first upsert isNew=%v err=%v", isNew, err)
	}
	_ = tx.Commit()

	v2 := randVec(rng, testDims)
	tx = e.Begin(true)
	pos, isNew, err := e.Upsert(tx, 1, 42, v2, MetaRow{scoreCol: Float(2)})
	if err != nil || isNew {
		t.Fatalf("second upsert isNew=%v err=%v", isNew, err)
	}
	_ = tx.Commit()

	snap := e.Snapshot()
	rec, err := e.Fetch(1, pos, nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	for j := range v2 {
		if rec.Vec[j] != v2[j] {
			t.Fatalf("upsert did not replace vector at dim %d", j)
		}
	}
	if !rec.Meta[scoreCol].equal(Float(2)) {
		t.Fatalf("upsert did not replace meta, got %+v", rec.Meta[scoreCol])
	}
	st, _ := e.CollectionStats(1)
	if st.LivePoints != 1 {
		t.Fatalf("after upsert live=%d, want 1", st.LivePoints)
	}
}

func TestUpdateMetaInPlace(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(10))
	pos := mustInsert(t, e, 1, 5, randVec(rng, testDims), MetaRow{scoreCol: Float(1)})
	tx := e.Begin(true)
	if err := e.UpdateMeta(tx, 1, 5, MetaRow{scoreCol: Float(9)}); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	snap := e.Snapshot()
	rec, _ := e.Fetch(1, pos, nil, snap)
	if !rec.Meta[scoreCol].equal(Float(9)) {
		t.Fatalf("update meta failed, got %+v", rec.Meta[scoreCol])
	}
}

func TestColumnStats(t *testing.T) {
	e := NewEngine()
	_ = e.CreateCollection(testDef(1, ElemFP32))
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 100; i++ {
		meta := MetaRow{scoreCol: Float(float64(i))}
		if i%5 == 0 {
			meta = MetaRow{} // null score
		}
		mustInsert(t, e, 1, PointID(i+1), randVec(rng, testDims), meta)
	}
	if err := e.Analyze(1); err != nil {
		t.Fatal(err)
	}
	cs, err := e.ColumnStats(1, scoreCol)
	if err != nil {
		t.Fatal(err)
	}
	if cs.NullFraction < 0.18 || cs.NullFraction > 0.22 {
		t.Fatalf("null fraction %v, want ~0.20", cs.NullFraction)
	}
	if cs.Min.F != 1 { // score 0 is null (i%5==0), smallest non-null is 1
		t.Fatalf("min score %v, want 1", cs.Min.F)
	}
	if cs.Max.F != 99 {
		t.Fatalf("max score %v, want 99", cs.Max.F)
	}
	if len(cs.Histogram) == 0 {
		t.Fatal("expected a histogram")
	}
}

func TestElementCodecsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		elem ElemType
		tol  float32
	}{
		{"fp32", ElemFP32, 0},
		{"fp16", ElemFP16, 0.01},
		{"int8", ElemInt8, 0.02},
	}
	for ci, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine()
			def := testDef(uint64(ci+1), tc.elem)
			def.Int8Scale = 1.0 / 127.0 // values in [0,1) map cleanly
			if err := e.CreateCollection(def); err != nil {
				t.Fatal(err)
			}
			rng := rand.New(rand.NewSource(int64(100 + ci)))
			v := randVec(rng, testDims)
			pos := mustInsert(t, e, uint64(ci+1), 1, v, nil)
			buf := make([]float32, testDims)
			if err := e.FetchVector(uint64(ci+1), pos, buf); err != nil {
				t.Fatal(err)
			}
			for j := range v {
				d := buf[j] - v[j]
				if d < 0 {
					d = -d
				}
				if d > tc.tol {
					t.Fatalf("%s dim %d: |%v-%v|=%v > tol %v", tc.name, j, buf[j], v[j], d, tc.tol)
				}
			}
		})
	}
}

func TestStrideMath(t *testing.T) {
	// fp32 D=768 -> 3072; fp16 -> 1536; int8 -> 768; binary -> 96 (spec 04 §3.3).
	cases := []struct {
		elem ElemType
		dims uint32
		want uint32
	}{
		{ElemFP32, 768, 3072},
		{ElemFP16, 768, 1536},
		{ElemInt8, 768, 768},
		{ElemBinary, 768, 96},
		{ElemFP32, 100, 416}, // 400 -> round up to 32 multiple = 416
	}
	for _, c := range cases {
		if got := computeStride(c.elem, c.dims); got != c.want {
			t.Fatalf("stride(%d,%d)=%d want %d", c.elem, c.dims, got, c.want)
		}
	}
}
