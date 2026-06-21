package verify

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"

	"github.com/tamnd/vec/distance"
	"github.com/tamnd/vec/vfs"
)

func randVecT(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

// naiveFlat is an independent brute-force top-k by L2, used to cross-check
// FlatSearch. It sorts every position and takes the first k, tie-breaking by
// position.
func naiveFlat(vecs [][]float32, q []float32, k int) []int64 {
	type s struct {
		pos  int64
		dist float32
	}
	all := make([]s, len(vecs))
	for i, v := range vecs {
		all[i] = s{int64(i), distance.L2SquaredFloat32(q, v)}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].dist != all[j].dist {
			return all[i].dist < all[j].dist
		}
		return all[i].pos < all[j].pos
	})
	if k > len(all) {
		k = len(all)
	}
	out := make([]int64, k)
	for i := 0; i < k; i++ {
		out[i] = all[i].pos
	}
	return out
}

func TestFlatSearchMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	vecs := make([][]float32, 500)
	for i := range vecs {
		vecs[i] = randVecT(rng, 16)
	}
	for trial := 0; trial < 20; trial++ {
		q := randVecT(rng, 16)
		got := FlatSearch(vecs, q, 10, distance.L2)
		want := naiveFlat(vecs, q, 10)
		if len(got) != len(want) {
			t.Fatalf("len %d != %d", len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("trial %d pos %d: got %d want %d", trial, i, got[i], want[i])
			}
		}
	}
}

func TestFlatSearchEdgeCases(t *testing.T) {
	if FlatSearch(nil, []float32{1}, 5, distance.L2) != nil {
		t.Fatal("empty set should return nil")
	}
	vecs := [][]float32{{0}, {1}, {2}}
	got := FlatSearch(vecs, []float32{0}, 10, distance.L2) // k > n
	if len(got) != 3 {
		t.Fatalf("k>n should clamp to n, got %d", len(got))
	}
	if FlatSearch(vecs, []float32{0}, 0, distance.L2) != nil {
		t.Fatal("k=0 should return nil")
	}
}

func TestMeasureRecall(t *testing.T) {
	cases := []struct {
		ann, flat []int64
		want      float64
	}{
		{[]int64{1, 2, 3}, []int64{1, 2, 3}, 1.0},
		{[]int64{1, 2, 9}, []int64{1, 2, 3}, 2.0 / 3.0},
		{[]int64{7, 8, 9}, []int64{1, 2, 3}, 0.0},
		{[]int64{}, []int64{}, 1.0}, // empty truth scores 1
	}
	for i, c := range cases {
		if got := MeasureRecall(c.ann, c.flat); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("case %d: recall %v want %v", i, got, c.want)
		}
	}
}

func TestMeasureRecallSet(t *testing.T) {
	ann := [][]int64{{1, 2}, {3, 9}}
	flat := [][]int64{{1, 2}, {3, 4}}
	got := MeasureRecallSet(ann, flat) // (1.0 + 0.5)/2
	if math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("mean recall %v want 0.75", got)
	}
}

func TestRefModelGetUpsertDelete(t *testing.T) {
	m := NewRefModel(distance.L2)
	if err := m.Upsert(ModelPoint{ID: 1, Vec: []float32{1, 2}}); err != nil {
		t.Fatal(err)
	}
	// Idempotent upsert keeps one copy.
	_ = m.Upsert(ModelPoint{ID: 1, Vec: []float32{1, 2}})
	if m.Len() != 1 {
		t.Fatalf("len = %d, want 1", m.Len())
	}
	p, ok, _ := m.Get(1)
	if !ok || p.Vec[1] != 2 {
		t.Fatalf("get = %+v ok=%v", p, ok)
	}
	// Stored vector is a copy: mutating the input must not change the model.
	in := []float32{5, 6}
	_ = m.Upsert(ModelPoint{ID: 2, Vec: in})
	in[0] = 99
	got, _, _ := m.Get(2)
	if got.Vec[0] != 5 {
		t.Fatalf("model vector aliased the input: %v", got.Vec)
	}
	_ = m.Delete(1)
	if _, ok, _ := m.Get(1); ok {
		t.Fatal("delete did not remove the point")
	}
}

func TestRefModelQueryOrderAndFilter(t *testing.T) {
	m := NewRefModel(distance.L2)
	_ = m.Upsert(ModelPoint{ID: 1, Vec: []float32{0, 0}, Attrs: Attrs{"cat": 1}})
	_ = m.Upsert(ModelPoint{ID: 2, Vec: []float32{1, 0}, Attrs: Attrs{"cat": 2}})
	_ = m.Upsert(ModelPoint{ID: 3, Vec: []float32{5, 0}, Attrs: Attrs{"cat": 1}})

	res, _ := m.Query([]float32{0, 0}, 3, QueryOpts{})
	if res[0].ID != 1 || res[1].ID != 2 || res[2].ID != 3 {
		t.Fatalf("order wrong: %+v", res)
	}
	// Filter to cat==1 keeps points 1 and 3 only.
	f := func(a Attrs) bool { return a["cat"] == 1 }
	res, _ = m.Query([]float32{0, 0}, 10, QueryOpts{Filter: f})
	if len(res) != 2 || res[0].ID != 1 || res[1].ID != 3 {
		t.Fatalf("filtered result wrong: %+v", res)
	}
}

func TestConformanceCleanOnTwoRefs(t *testing.T) {
	// Two reference models must never diverge: this exercises the driver itself.
	rng := rand.New(rand.NewSource(7))
	a := NewRefModel(distance.L2)
	b := NewRefModel(distance.L2)
	ops := GenerateOps(rng, 400, GenSchema{Dim: 8, MaxID: 40, QueryK: 5, WithDel: true})
	div := RunConformance(a, b, ops, FlatRecall(distance.L2))
	if len(div) != 0 {
		t.Fatalf("two reference models diverged: %v", div[0])
	}
}

// brokenCollection is a RefModel whose Delete is a no-op, so the conformance
// driver must catch the divergence against a correct reference.
type brokenCollection struct{ *RefModel }

func (b brokenCollection) Delete(id uint64) error { return nil }

func TestConformanceCatchesDivergence(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	real := brokenCollection{NewRefModel(distance.L2)}
	ref := NewRefModel(distance.L2)
	ops := GenerateOps(rng, 600, GenSchema{Dim: 6, MaxID: 20, QueryK: 5, WithDel: true})
	div := RunConformance(real, ref, ops, FlatRecall(distance.L2))
	if len(div) == 0 {
		t.Fatal("expected the broken delete to cause a query divergence")
	}
}

func TestInsertionRelation(t *testing.T) {
	m := NewRefModel(distance.L2)
	rng := rand.New(rand.NewSource(3))
	for i := uint64(1); i <= 200; i++ {
		_ = m.Upsert(ModelPoint{ID: i, Vec: randVecT(rng, 8)})
	}
	q := randVecT(rng, 8)
	// A point at q itself is strictly closest.
	if err := InsertionRelation(m, q, append([]float32(nil), q...), 99999); err != nil {
		t.Fatal(err)
	}
}

func TestDeletionRelation(t *testing.T) {
	m := NewRefModel(distance.L2)
	rng := rand.New(rand.NewSource(4))
	for i := uint64(1); i <= 200; i++ {
		_ = m.Upsert(ModelPoint{ID: i, Vec: randVecT(rng, 8)})
	}
	q := randVecT(rng, 8)
	top, _ := m.Query(q, 1, QueryOpts{})
	if err := DeletionRelation(m, q, top[0].ID, []int{1, 5, 10}); err != nil {
		t.Fatal(err)
	}
}

func TestFilterRelation(t *testing.T) {
	m := NewRefModel(distance.L2)
	rng := rand.New(rand.NewSource(5))
	passes := func(id uint64) bool { return id%2 == 0 }
	for i := uint64(1); i <= 300; i++ {
		_ = m.Upsert(ModelPoint{ID: i, Vec: randVecT(rng, 8), Attrs: Attrs{"even": i%2 == 0}})
	}
	q := randVecT(rng, 8)
	filter := func(a Attrs) bool { return a["even"] == true }
	if err := FilterRelation(m, q, 10, 30, filter, passes); err != nil {
		t.Fatal(err)
	}
}

func TestReindexRelation(t *testing.T) {
	m := NewRefModel(distance.L2)
	ref := NewRefModel(distance.L2)
	rng := rand.New(rand.NewSource(6))
	for i := uint64(1); i <= 100; i++ {
		v := randVecT(rng, 8)
		_ = m.Upsert(ModelPoint{ID: i, Vec: v})
		_ = ref.Upsert(ModelPoint{ID: i, Vec: v})
	}
	queries := make([][]float32, 20)
	for i := range queries {
		queries[i] = randVecT(rng, 8)
	}
	// Reindex is a no-op for the reference model; recall stays at 1.0.
	if err := ReindexRelation(m, ref, queries, 10, func() error { return nil }, 0.01); err != nil {
		t.Fatal(err)
	}
}

// --- Crash harness over a toy append-only store -----------------------------

// toyDB is a minimal log-structured store used to exercise the crash harness
// without the real engine. Each commit appends a fixed-size record and syncs, so
// the durable prefix after a crash is a whole number of committed records.
type toyDB struct {
	f   vfs.File
	off int64
}

const recSize = 8

func openToy(fs vfs.FS) (CrashDB, error) {
	f, err := fs.Open("toy.db", vfs.OpenReadWrite|vfs.OpenCreate)
	if err != nil {
		return nil, err
	}
	sz, _ := f.Size()
	// Round down to whole records: a torn tail is ignored on recovery.
	sz -= sz % recSize
	return &toyDB{f: f, off: sz}, nil
}

func (db *toyDB) commit(v uint64) error {
	var b [recSize]byte
	binary.LittleEndian.PutUint64(b[:], v)
	if _, err := db.f.WriteAt(b[:], db.off); err != nil {
		return err
	}
	if err := db.f.Sync(vfs.SyncData); err != nil {
		return err
	}
	db.off += recSize
	return nil
}

func (db *toyDB) records() ([]uint64, error) {
	sz, _ := db.f.Size()
	sz -= sz % recSize
	out := make([]uint64, 0, sz/recSize)
	for o := int64(0); o < sz; o += recSize {
		var b [recSize]byte
		if _, err := db.f.ReadAt(b[:], o); err != nil {
			return nil, err
		}
		out = append(out, binary.LittleEndian.Uint64(b[:]))
	}
	return out, nil
}

func (db *toyDB) Close() error { return db.f.Close() }

func TestRunExhaustiveCrashCleanStore(t *testing.T) {
	// Commit 1,2,3. After a crash at any sync boundary, the recovered records
	// must be a prefix of [1,2,3]: a whole-record durable prefix, never a gap.
	h := CrashHarness{
		NewBacking: func() vfs.FS { return vfs.NewMem() },
		Open:       openToy,
		Workload: func(d CrashDB) error {
			db := d.(*toyDB)
			for _, v := range []uint64{1, 2, 3} {
				if err := db.commit(v); err != nil {
					return err
				}
			}
			return nil
		},
		Verify: func(d CrashDB, crashAt int) error {
			recs, err := d.(*toyDB).records()
			if err != nil {
				return err
			}
			want := []uint64{1, 2, 3}
			if len(recs) > len(want) {
				return errPrefix(recs, want)
			}
			for i, r := range recs {
				if r != want[i] {
					return errPrefix(recs, want)
				}
			}
			return nil
		},
	}
	failures, err := RunExhaustiveCrash(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("clean store failed crash verification: %+v", failures)
	}
}

func errPrefix(got []uint64, want []uint64) error {
	return fmt.Errorf("recovered records %v are not a prefix of committed %v", got, want)
}

func TestRunExhaustiveCrashCatchesGap(t *testing.T) {
	// A buggy store that writes record 2 before record 1 (out of order) must be
	// caught: a crash between the two syncs leaves record 2 durable without 1,
	// which is not a valid prefix.
	h := CrashHarness{
		NewBacking: func() vfs.FS { return vfs.NewMem() },
		Open:       openToy,
		Workload: func(d CrashDB) error {
			db := d.(*toyDB)
			// Write record at offset 8 (the second slot) and sync, then offset 0.
			var b [recSize]byte
			binary.LittleEndian.PutUint64(b[:], 2)
			if _, err := db.f.WriteAt(b[:], recSize); err != nil {
				return err
			}
			if err := db.f.Sync(vfs.SyncData); err != nil {
				return err
			}
			binary.LittleEndian.PutUint64(b[:], 1)
			if _, err := db.f.WriteAt(b[:], 0); err != nil {
				return err
			}
			return db.f.Sync(vfs.SyncData)
		},
		Verify: func(d CrashDB, crashAt int) error {
			recs, err := d.(*toyDB).records()
			if err != nil {
				return err
			}
			// Record 1 must be durable before record 2 is.
			if len(recs) >= 2 && recs[0] != 1 {
				return errors.New("record 2 durable without record 1")
			}
			if len(recs) >= 1 && recs[0] == 2 {
				return errors.New("record 2 durable without record 1")
			}
			return nil
		},
	}
	failures, err := RunExhaustiveCrash(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) == 0 {
		t.Fatal("expected the out-of-order store to fail crash verification")
	}
}

func TestFaultSyncFail(t *testing.T) {
	fs := NewFaultFS(vfs.NewMem(), FaultConfig{Mode: FaultSyncFail, SyncTarget: 1})
	f, err := fs.Open("x", vfs.OpenReadWrite|vfs.OpenCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(vfs.SyncData); !errors.Is(err, ErrInjectedSync) {
		t.Fatalf("want ErrInjectedSync, got %v", err)
	}
}

func TestFaultWriteTear(t *testing.T) {
	mem := vfs.NewMem()
	fs := NewFaultFS(mem, FaultConfig{Mode: FaultWriteTear, TearAt: 2})
	f, _ := fs.Open("x", vfs.OpenReadWrite|vfs.OpenCreate)
	if _, err := f.WriteAt([]byte("abcd"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(vfs.SyncData); err != nil {
		t.Fatal(err)
	}
	// Only the first 2 bytes reached the durable store.
	g, _ := mem.Open("x", vfs.OpenRead)
	buf := make([]byte, 4)
	n, _ := g.ReadAt(buf, 0)
	if n < 2 || string(buf[:2]) != "ab" {
		t.Fatalf("torn write did not keep the prefix: %q n=%d", buf, n)
	}
	if buf[2] != 0 || buf[3] != 0 {
		t.Fatalf("torn write persisted past the tear: %q", buf)
	}
}

func TestFaultSyncDropLosesData(t *testing.T) {
	mem := vfs.NewMem()
	fs := NewFaultFS(mem, FaultConfig{Mode: FaultSyncDrop, SyncTarget: 1})
	f, _ := fs.Open("x", vfs.OpenReadWrite|vfs.OpenCreate)
	_, _ = f.WriteAt([]byte("data"), 0)
	if err := f.Sync(vfs.SyncData); err != nil {
		t.Fatalf("sync drop should report success, got %v", err)
	}
	g, _ := mem.Open("x", vfs.OpenRead)
	buf := make([]byte, 4)
	n, _ := g.ReadAt(buf, 0)
	if n != 0 {
		t.Fatalf("dropped sync should persist nothing, got %d bytes", n)
	}
}

func TestFaultReadYourWrites(t *testing.T) {
	// Before a sync, a reader on the same file sees its own buffered writes.
	fs := NewFaultFS(vfs.NewMem(), FaultConfig{Mode: FaultNone})
	f, _ := fs.Open("x", vfs.OpenReadWrite|vfs.OpenCreate)
	_, _ = f.WriteAt([]byte("xyz"), 0)
	buf := make([]byte, 3)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "xyz" {
		t.Fatalf("read-your-writes failed: %q", buf)
	}
}

func TestGenerateOpsDeterministic(t *testing.T) {
	a := GenerateOps(rand.New(rand.NewSource(42)), 100, GenSchema{Dim: 4, MaxID: 10, WithDel: true})
	b := GenerateOps(rand.New(rand.NewSource(42)), 100, GenSchema{Dim: 4, MaxID: 10, WithDel: true})
	if len(a) != len(b) {
		t.Fatalf("len %d != %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].ID != b[i].ID {
			t.Fatalf("op %d differs across same-seed runs", i)
		}
	}
}
