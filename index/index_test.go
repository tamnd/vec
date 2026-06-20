package index

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/tamnd/vector/distance"
)

// randomVectors returns n d-dim vectors from a fixed seed.
func randomVectors(n, d int, seed int64) [][]float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, d)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		out[i] = v
	}
	return out
}

func vectorAtFn(vecs [][]float32) func(uint32) []float32 {
	return func(p uint32) []float32 { return vecs[p] }
}

// recallAt returns the fraction of flat's top-k positions that appear in got.
func recallAt(got, want []Candidate, k int) float64 {
	if k > len(want) {
		k = len(want)
	}
	if k == 0 {
		return 1
	}
	set := make(map[uint32]struct{}, len(got))
	for _, c := range got {
		set[c.Position] = struct{}{}
	}
	hit := 0
	for i := 0; i < k; i++ {
		if _, ok := set[want[i].Position]; ok {
			hit++
		}
	}
	return float64(hit) / float64(k)
}

func buildHNSW(t *testing.T, vecs [][]float32, metric Metric, params BuildParams) *HNSW {
	t.Helper()
	h, err := NewHNSW(HNSWConfig{Dim: len(vecs[0]), Metric: metric, M: params.M, EfConstruction: params.EfConstruction, Seed: params.Seed})
	if err != nil {
		t.Fatal(err)
	}
	positions := make([]uint32, len(vecs))
	for i := range positions {
		positions[i] = uint32(i)
	}
	if err := h.Build(context.Background(), positions, vectorAtFn(vecs), params); err != nil {
		t.Fatal(err)
	}
	return h
}

func buildFlat(t *testing.T, vecs [][]float32, metric Metric) *Flat {
	t.Helper()
	f := NewFlat(len(vecs[0]), metric)
	positions := make([]uint32, len(vecs))
	for i := range positions {
		positions[i] = uint32(i)
	}
	if err := f.Build(context.Background(), positions, vectorAtFn(vecs), BuildParams{Metric: metric}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestHNSWRecall(t *testing.T) {
	const n, dim, k = 4000, 64, 10
	vecs := randomVectors(n, dim, 42)
	params := BuildParams{M: 16, EfConstruction: 200, Metric: distance.L2Squared, Seed: 7}
	h := buildHNSW(t, vecs, distance.L2Squared, params)
	flat := buildFlat(t, vecs, distance.L2Squared)

	queries := randomVectors(100, dim, 99)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		hr, err := h.Search(ctx, q, k, nil, SearchParams{EfSearch: 64})
		if err != nil {
			t.Fatal(err)
		}
		fr, err := flat.Search(ctx, q, k, nil, SearchParams{})
		if err != nil {
			t.Fatal(err)
		}
		total += recallAt(hr, fr, k)
	}
	avg := total / float64(len(queries))
	if avg < 0.95 {
		t.Fatalf("recall@%d = %.3f, want >= 0.95", k, avg)
	}
}

func TestHNSWCosineRecall(t *testing.T) {
	const n, dim, k = 3000, 48, 10
	vecs := randomVectors(n, dim, 11)
	// Normalize for cosine.
	for _, v := range vecs {
		var s float32
		for _, x := range v {
			s += x * x
		}
		inv := float32(1.0 / math.Sqrt(float64(s)))
		for i := range v {
			v[i] *= inv
		}
	}
	params := BuildParams{M: 16, EfConstruction: 200, Metric: distance.Cosine, Seed: 3}
	h := buildHNSW(t, vecs, distance.Cosine, params)
	flat := buildFlat(t, vecs, distance.Cosine)
	queries := randomVectors(50, dim, 77)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		hr, _ := h.Search(ctx, q, k, nil, SearchParams{EfSearch: 80})
		fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
		total += recallAt(hr, fr, k)
	}
	if avg := total / float64(len(queries)); avg < 0.93 {
		t.Fatalf("cosine recall@%d = %.3f, want >= 0.93", k, avg)
	}
}

func TestHNSWDeterminism(t *testing.T) {
	const n, dim = 1500, 32
	vecs := randomVectors(n, dim, 5)
	params := BuildParams{M: 12, EfConstruction: 100, Metric: distance.L2Squared, Seed: 123}
	h1 := buildHNSW(t, vecs, distance.L2Squared, params)
	h2 := buildHNSW(t, vecs, distance.L2Squared, params)
	for pos := uint32(0); pos < n; pos++ {
		na, nb := h1.nodes[pos], h2.nodes[pos]
		if na.maxLevel != nb.maxLevel {
			t.Fatalf("pos %d level %d != %d", pos, na.maxLevel, nb.maxLevel)
		}
		for l := 0; l <= na.maxLevel; l++ {
			if len(na.neighbors[l]) != len(nb.neighbors[l]) {
				t.Fatalf("pos %d layer %d neighbor count mismatch", pos, l)
			}
			for i := range na.neighbors[l] {
				if na.neighbors[l][i] != nb.neighbors[l][i] {
					t.Fatalf("pos %d layer %d neighbor %d mismatch", pos, l, i)
				}
			}
		}
	}
}

func TestHNSWSmallGraphs(t *testing.T) {
	dim := 8
	h, _ := NewHNSW(HNSWConfig{Dim: dim, Metric: distance.L2Squared, Seed: 1})
	ctx := context.Background()
	// Empty graph.
	res, err := h.Search(ctx, make([]float32, dim), 5, nil, SearchParams{})
	if err != nil || res != nil {
		t.Fatalf("empty search = %v, %v", res, err)
	}
	// Single node.
	v := randomVectors(1, dim, 2)[0]
	if err := h.Insert(0, v); err != nil {
		t.Fatal(err)
	}
	res, _ = h.Search(ctx, v, 5, nil, SearchParams{})
	if len(res) != 1 || res[0].Position != 0 {
		t.Fatalf("single-node search = %v", res)
	}
	// Fewer nodes than k.
	for i := 1; i < 3; i++ {
		_ = h.Insert(uint32(i), randomVectors(1, dim, int64(i+10))[0])
	}
	res, _ = h.Search(ctx, v, 10, nil, SearchParams{})
	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}
}

func TestHNSWDeleteAndTombstone(t *testing.T) {
	const n, dim, k = 2000, 32, 10
	vecs := randomVectors(n, dim, 8)
	h := buildHNSW(t, vecs, distance.L2Squared, BuildParams{M: 16, EfConstruction: 200, Metric: distance.L2Squared, Seed: 4})
	ctx := context.Background()

	// Delete the would-be nearest neighbor of a query and confirm it is excluded.
	q := vecs[100]
	before, _ := h.Search(ctx, q, k, nil, SearchParams{EfSearch: 64})
	if len(before) == 0 || before[0].Position != 100 {
		t.Fatalf("expected pos 100 as nearest, got %v", before[:1])
	}
	if err := h.Delete(100); err != nil {
		t.Fatal(err)
	}
	after, _ := h.Search(ctx, q, k, nil, SearchParams{EfSearch: 64})
	for _, c := range after {
		if c.Position == 100 {
			t.Fatal("deleted pos 100 still returned")
		}
	}
	if h.Stats().TombstoneCount != 1 {
		t.Fatalf("tombstone count = %d, want 1", h.Stats().TombstoneCount)
	}
}

func TestHNSWEntrypointRepair(t *testing.T) {
	const n, dim = 500, 16
	vecs := randomVectors(n, dim, 6)
	h := buildHNSW(t, vecs, distance.L2Squared, BuildParams{M: 8, EfConstruction: 100, Metric: distance.L2Squared, Seed: 9})
	ep := h.Stats().EntrypointPos
	if err := h.Delete(ep); err != nil {
		t.Fatal(err)
	}
	if h.Stats().EntrypointPos == ep {
		t.Fatal("entrypoint not repaired after deleting it")
	}
	// Search still works.
	res, _ := h.Search(context.Background(), vecs[0], 5, nil, SearchParams{EfSearch: 32})
	if len(res) == 0 {
		t.Fatal("search returned nothing after entrypoint repair")
	}
}

func TestHNSWFilter(t *testing.T) {
	const n, dim, k = 3000, 32, 10
	vecs := randomVectors(n, dim, 13)
	h := buildHNSW(t, vecs, distance.L2Squared, BuildParams{M: 16, EfConstruction: 200, Metric: distance.L2Squared, Seed: 2})
	flat := buildFlat(t, vecs, distance.L2Squared)
	ctx := context.Background()

	// 10% selectivity: allow every 10th position.
	allowed := make([]uint32, 0, n/10)
	for i := 0; i < n; i += 10 {
		allowed = append(allowed, uint32(i))
	}
	filter := NewSliceBitmap(allowed)

	q := randomVectors(1, dim, 555)[0]
	hr, _ := h.Search(ctx, q, k, filter, SearchParams{EfSearch: 200})
	fr, _ := flat.Search(ctx, q, k, filter, SearchParams{})
	for _, c := range hr {
		if !filter.Contains(c.Position) {
			t.Fatalf("filtered search returned disallowed pos %d", c.Position)
		}
	}
	if r := recallAt(hr, fr, k); r < 0.9 {
		t.Fatalf("filtered recall = %.3f, want >= 0.9", r)
	}
}

func TestFlatExact(t *testing.T) {
	const n, dim, k = 500, 16, 5
	vecs := randomVectors(n, dim, 21)
	flat := buildFlat(t, vecs, distance.L2Squared)
	q := vecs[42]
	res, _ := flat.Search(context.Background(), q, k, nil, SearchParams{})
	if res[0].Position != 42 || res[0].Distance > 1e-5 {
		t.Fatalf("flat nearest = %v, want pos 42 dist 0", res[0])
	}
}
