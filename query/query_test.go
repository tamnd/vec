package query

import (
	"context"
	"math/rand"
	"testing"

	"github.com/tamnd/vec/distance"
	"github.com/tamnd/vec/hybrid"
	"github.com/tamnd/vec/index"
	"github.com/tamnd/vec/storage"
)

const (
	qDims    = 16
	scoreCol = storage.ColID(2)
	tagCol   = storage.ColID(3)
)

// buildHarness inserts n random points into a fresh engine, builds an HNSW index
// over them, and returns a query.Collection plus the raw vectors for oracle checks.
func buildHarness(t *testing.T, n int, seed int64) (*Collection, [][]float32, []uint32) {
	t.Helper()
	e := storage.NewEngine()
	def := storage.CollectionDef{
		ID:     1,
		Name:   "docs",
		Dims:   qDims,
		Elem:   storage.ElemFP32,
		Metric: distance.L2Squared,
		Columns: []storage.ColumnDef{
			{ID: scoreCol, Name: "score", Type: storage.ColFloat64},
			{ID: tagCol, Name: "tag", Type: storage.ColText, Nullable: true},
		},
		SegmentCapacity: 256,
	}
	if err := e.CreateCollection(def); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(seed))
	vecs := make([][]float32, n)
	positions := make([]uint32, n)
	for i := 0; i < n; i++ {
		v := randVec(rng, qDims)
		vecs[i] = v
		tag := "even"
		if i%2 == 1 {
			tag = "odd"
		}
		tx := e.Begin(true)
		pos, err := e.Insert(tx, 1, storage.PointID(i+1), v, storage.MetaRow{
			scoreCol: storage.Float(float64(i)),
			tagCol:   storage.Text(tag),
		})
		if err != nil {
			tx.Abort()
			t.Fatalf("insert %d: %v", i, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		positions[i] = pos
	}

	h, err := index.NewHNSW(index.HNSWConfig{Dim: qDims, Metric: distance.L2Squared, M: 16, EfConstruction: 200, Seed: 42})
	if err != nil {
		t.Fatal(err)
	}
	byPos := make(map[uint32][]float32, n)
	for i, p := range positions {
		byPos[p] = vecs[i]
	}
	vectorAt := func(p uint32) []float32 { return byPos[p] }
	if err := h.Build(context.Background(), positions, vectorAt, index.BuildParams{M: 16, EfConstruction: 200, Metric: distance.L2Squared}); err != nil {
		t.Fatal(err)
	}

	coll := &Collection{
		Engine:         e,
		CollID:         1,
		Dims:           qDims,
		Metric:         distance.L2Squared,
		Index:          h,
		IndexKind:      PathHNSW,
		M:              16,
		EfConstruction: 200,
		MetaCols:       map[string]storage.ColID{"score": scoreCol, "tag": tagCol},
	}
	return coll, vecs, positions
}

func randVec(rng *rand.Rand, d int) []float32 {
	v := make([]float32, d)
	for i := range v {
		v[i] = rng.Float32()
	}
	return v
}

// bruteTopK returns the k nearest point ids (1-based, matching insertion) to query
// under L2Squared, the recall oracle.
func bruteTopK(query []float32, vecs [][]float32, k int) []uint64 {
	type pd struct {
		id uint64
		d  float32
	}
	all := make([]pd, len(vecs))
	for i, v := range vecs {
		all[i] = pd{uint64(i + 1), distance.L2SquaredFloat32(query, v)}
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].d < all[i].d || (all[j].d == all[i].d && all[j].id < all[i].id) {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	out := make([]uint64, 0, k)
	for i := 0; i < k && i < len(all); i++ {
		out = append(out, all[i].id)
	}
	return out
}

func TestExecuteRecallAgainstOracle(t *testing.T) {
	coll, vecs, _ := buildHarness(t, 6000, 1)
	exec := NewExecutor(coll)
	planner := NewPlanner(coll, 16)
	rng := rand.New(rand.NewSource(99))

	const k = 10
	queries := 20
	var hit, total int
	for q := 0; q < queries; q++ {
		query := randVec(rng, qDims)
		plan, err := planner.Plan(BoundQuery{Vector: query, K: k, Metric: distance.L2Squared, EfSearch: 200})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Path != PathHNSW {
			t.Fatalf("query %d: expected HNSW path, got %s", q, plan.Path)
		}
		rs, err := exec.Execute(context.Background(), plan, query)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if len(rs.Rows) != k {
			t.Fatalf("query %d: got %d rows want %d", q, len(rs.Rows), k)
		}
		want := bruteTopK(query, vecs, k)
		wantSet := make(map[uint64]bool, k)
		for _, id := range want {
			wantSet[id] = true
		}
		for _, r := range rs.Rows {
			if wantSet[uint64(r.PointID)] {
				hit++
			}
			total++
		}
		// Distances must be non-decreasing (nearest first).
		for i := 1; i < len(rs.Rows); i++ {
			if rs.Rows[i].Distance < rs.Rows[i-1].Distance {
				t.Fatalf("query %d: results not sorted at %d", q, i)
			}
		}
	}
	recall := float64(hit) / float64(total)
	if recall < 0.90 {
		t.Fatalf("recall@%d = %.3f, want >= 0.90", k, recall)
	}
}

func TestExecuteDeterministic(t *testing.T) {
	coll, _, _ := buildHarness(t, 800, 2)
	exec := NewExecutor(coll)
	planner := NewPlanner(coll, 16)
	query := randVec(rand.New(rand.NewSource(7)), qDims)
	plan, err := planner.Plan(BoundQuery{Vector: query, K: 10, Metric: distance.L2Squared, EfSearch: 100})
	if err != nil {
		t.Fatal(err)
	}
	first, err := exec.Execute(context.Background(), plan, query)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := exec.Execute(context.Background(), plan, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Rows) != len(first.Rows) {
			t.Fatalf("run %d length differs", i)
		}
		for j := range got.Rows {
			if got.Rows[j].PointID != first.Rows[j].PointID {
				t.Fatalf("run %d row %d id differs: %d vs %d", i, j, got.Rows[j].PointID, first.Rows[j].PointID)
			}
		}
	}
}

func TestExecuteFilterCorrectness(t *testing.T) {
	coll, vecs, _ := buildHarness(t, 1000, 3)
	exec := NewExecutor(coll)
	planner := NewPlanner(coll, 16)
	query := randVec(rand.New(rand.NewSource(5)), qDims)

	// Only "even" tags (inserted at even i, point ids odd: id=i+1).
	pred := storage.Compare{Col: tagCol, Op: storage.OpEq, Lit: storage.Text("even")}
	plan, err := planner.Plan(BoundQuery{
		Vector:      query,
		K:           10,
		Metric:      distance.L2Squared,
		Predicate:   pred,
		Selectivity: 0.5,
		EfSearch:    200,
		Project:     []string{"tag"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := exec.Execute(context.Background(), plan, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rows) == 0 {
		t.Fatal("no rows survived the filter")
	}
	for _, r := range rs.Rows {
		// Even-tag points were inserted at even i, so point id = i+1 is odd.
		if uint64(r.PointID)%2 == 0 {
			t.Fatalf("point id %d should not pass the even filter", r.PointID)
		}
		if tv, ok := r.Meta["tag"]; ok {
			if tv.S != "even" {
				t.Fatalf("projected tag = %q want even", tv.S)
			}
		}
	}
	_ = vecs
}

func TestPlannerChoosesFlatForSmallN(t *testing.T) {
	coll, _, _ := buildHarness(t, 400, 4)
	planner := NewPlanner(coll, 16)
	query := randVec(rand.New(rand.NewSource(1)), qDims)
	plan, err := planner.Plan(BoundQuery{Vector: query, K: 10, Metric: distance.L2Squared})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != PathFlat {
		t.Fatalf("small collection should use flat, got %s", plan.Path)
	}
	if plan.EstRecall != 1.0 {
		t.Fatalf("flat recall should be 1.0, got %v", plan.EstRecall)
	}
}

func TestFilterStrategyThresholds(t *testing.T) {
	pl := &Planner{}
	cases := []struct {
		sel  float64
		want FilterStrategy
	}{
		{0.01, FilterPre},
		{0.05, FilterPre},
		{0.30, FilterIn},
		{0.60, FilterPost},
		{0.95, FilterPost},
	}
	for _, c := range cases {
		if got := pl.filterStrategy(true, c.sel); got != c.want {
			t.Fatalf("sel %.2f: got %s want %s", c.sel, got, c.want)
		}
	}
	if got := pl.filterStrategy(false, 0.01); got != FilterNone {
		t.Fatalf("no predicate should be FilterNone, got %s", got)
	}
}

func TestPlanCacheHit(t *testing.T) {
	coll, _, _ := buildHarness(t, 400, 6)
	planner := NewPlanner(coll, 16)
	q := BoundQuery{Vector: randVec(rand.New(rand.NewSource(2)), qDims), K: 10, Metric: distance.L2Squared}
	if _, err := planner.Plan(q); err != nil {
		t.Fatal(err)
	}
	if n := planner.cache.Len(); n != 1 {
		t.Fatalf("cache len after first plan = %d want 1", n)
	}
	// A second structurally identical query (different vector) hits the cache.
	q2 := q
	q2.Vector = randVec(rand.New(rand.NewSource(3)), qDims)
	if _, err := planner.Plan(q2); err != nil {
		t.Fatal(err)
	}
	if n := planner.cache.Len(); n != 1 {
		t.Fatalf("cache len after equivalent plan = %d want 1", n)
	}
}

func TestExecutePartialOnCancel(t *testing.T) {
	coll, _, _ := buildHarness(t, 300, 8)
	exec := NewExecutor(coll)
	planner := NewPlanner(coll, 16)
	query := randVec(rand.New(rand.NewSource(4)), qDims)
	// Force the flat path so Open does not fail on the cancelled context; the drive
	// loop then observes cancellation and returns a partial result.
	plan, err := planner.Plan(BoundQuery{Vector: query, K: 10, Metric: distance.L2Squared})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rs, err := exec.Execute(ctx, plan, query)
	if err != nil {
		t.Fatalf("cancelled query should not hard-error: %v", err)
	}
	if !rs.Partial {
		t.Fatal("cancelled query should report a partial result")
	}
}

func TestPlanRejectsBadInput(t *testing.T) {
	coll, _, _ := buildHarness(t, 100, 9)
	planner := NewPlanner(coll, 4)
	query := randVec(rand.New(rand.NewSource(1)), qDims)
	if _, err := planner.Plan(BoundQuery{Vector: query, K: 0, Metric: distance.L2Squared}); err != ErrInvalidK {
		t.Fatalf("k=0 should be ErrInvalidK, got %v", err)
	}
	if _, err := planner.Plan(BoundQuery{Vector: query[:3], K: 10, Metric: distance.L2Squared}); err != ErrDimensionMismatch {
		t.Fatalf("short vector should be ErrDimensionMismatch, got %v", err)
	}
}

// TestExecuteHybridFusion drives the dense-plus-lexical fusion path: a dense plan
// fused with an extra ranked list that strongly boosts one known position. The fused
// result must surface that position and the FuseCombined stat must be populated.
func TestExecuteHybridFusion(t *testing.T) {
	coll, vecs, positions := buildHarness(t, 6000, 9)
	planner := NewPlanner(coll, 16)
	exec := NewExecutor(coll)

	// Query with an existing point's own vector so its position is the dense top hit.
	target := 1234
	query := vecs[target]
	plan, err := planner.Plan(BoundQuery{Vector: query, K: 10, Metric: distance.L2Squared, EfSearch: 200})
	if err != nil {
		t.Fatal(err)
	}

	// An extra modality list that also ranks the target position first.
	extra := []hybrid.ScoredPos{
		{Pos: positions[target], Score: 100},
		{Pos: positions[target+1], Score: 50},
	}
	rs, err := exec.ExecuteHybrid(context.Background(), HybridRequest{
		Plan:   plan,
		Vector: query,
		Extra:  [][]hybrid.ScoredPos{extra},
		Method: hybrid.FusionRRF,
		RRFK:   hybrid.DefaultRRFK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rows) == 0 {
		t.Fatal("expected fused rows")
	}
	if rs.Rows[0].PointID != storage.PointID(target+1) {
		t.Fatalf("expected target point %d fused first, got %d", target+1, rs.Rows[0].PointID)
	}
	if rs.Stats.FuseCombined == 0 {
		t.Fatal("FuseCombined stat should be populated")
	}
}

// TestExecuteHybridPostFilter checks that a post-filter predicate prunes the fused
// result to matching positions only.
func TestExecuteHybridPostFilter(t *testing.T) {
	coll, vecs, _ := buildHarness(t, 6000, 11)
	planner := NewPlanner(coll, 16)
	exec := NewExecutor(coll)

	query := vecs[42]
	plan, err := planner.Plan(BoundQuery{
		Vector:    query,
		K:         10,
		Metric:    distance.L2Squared,
		EfSearch:  200,
		Predicate: storage.Compare{Col: tagCol, Op: storage.OpEq, Lit: storage.Text("odd")},
		Project:   []string{"tag"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Force the post-filter strategy so fusion runs before the predicate prune.
	plan.Filter = FilterPost
	rs, err := exec.ExecuteHybrid(context.Background(), HybridRequest{
		Plan:   plan,
		Vector: query,
		Method: hybrid.FusionRRF,
		RRFK:   hybrid.DefaultRRFK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rows) == 0 {
		t.Fatal("expected fused rows")
	}
	for _, r := range rs.Rows {
		if tv, ok := r.Meta["tag"]; !ok || tv.S != "odd" {
			t.Fatalf("post-filter leaked non-odd row: %+v", r.Meta)
		}
	}
}
