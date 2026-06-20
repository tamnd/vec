package index

import (
	"context"
	"testing"

	"github.com/tamnd/vector/distance"
	"github.com/tamnd/vector/quant"
)

// trainPQForTest trains a PQ codec over vecs for navigation-codec tests.
func trainPQForTest(t *testing.T, vecs [][]float32, dim, m int) Codec {
	t.Helper()
	data := make([]float32, len(vecs)*dim)
	for i, v := range vecs {
		copy(data[i*dim:(i+1)*dim], v)
	}
	cb, err := quant.TrainPQ(data, len(vecs), dim, m, 8, 25)
	if err != nil {
		t.Fatal(err)
	}
	return quant.NewPQQuantizer(cb)
}

// positionsFor returns the dense positions 0..n-1.
func positionsFor(n int) []uint32 {
	p := make([]uint32, n)
	for i := range p {
		p[i] = uint32(i)
	}
	return p
}

// buildIVF constructs and builds an IVF index over vecs.
func buildIVF(t *testing.T, vecs [][]float32, cfg IVFConfig) *IVF {
	t.Helper()
	cfg.Dim = len(vecs[0])
	idx, err := NewIVF(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Build(context.Background(), positionsFor(len(vecs)), vectorAtFn(vecs), BuildParams{Metric: cfg.Metric, Seed: cfg.Seed}); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestIVFRecall(t *testing.T) {
	const n, dim, k = 4000, 64, 10
	vecs := randomVectors(n, dim, 42)
	idx := buildIVF(t, vecs, IVFConfig{Metric: distance.L2Squared, NList: 64, NProbe: 48, Seed: 7})
	flat := buildFlat(t, vecs, distance.L2Squared)

	queries := randomVectors(100, dim, 99)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		// Probe most cells so true neighbors in adjacent cells are reached; recall
		// approaches flat as nprobe grows toward nlist (spec 08 §5.3).
		ir, err := idx.Search(ctx, q, k, nil, SearchParams{NProbe: 48})
		if err != nil {
			t.Fatal(err)
		}
		fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
		total += recallAt(ir, fr, k)
	}
	if avg := total / float64(len(queries)); avg < 0.90 {
		t.Fatalf("IVF recall@%d = %.3f, want >= 0.90", k, avg)
	}
}

func TestIVFNProbeMonotonic(t *testing.T) {
	const n, dim, k = 3000, 48, 10
	vecs := randomVectors(n, dim, 71)
	idx := buildIVF(t, vecs, IVFConfig{Metric: distance.L2Squared, NList: 64, Seed: 7})
	flat := buildFlat(t, vecs, distance.L2Squared)
	queries := randomVectors(60, dim, 12)
	ctx := context.Background()
	recallFor := func(nprobe int) float64 {
		total := 0.0
		for _, q := range queries {
			ir, _ := idx.Search(ctx, q, k, nil, SearchParams{NProbe: nprobe})
			fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
			total += recallAt(ir, fr, k)
		}
		return total / float64(len(queries))
	}
	// More probes never lose recall (spec 08 §5.3 recall-cost tradeoff).
	low, high := recallFor(4), recallFor(48)
	if high < low {
		t.Fatalf("recall fell as nprobe rose: nprobe=4 %.3f, nprobe=48 %.3f", low, high)
	}
}

func TestIVFADCRecall(t *testing.T) {
	const n, dim, k = 4000, 64, 10
	vecs := randomVectors(n, dim, 24)
	idx := buildIVF(t, vecs, IVFConfig{Metric: distance.L2Squared, NList: 64, NProbe: 24, PQM: 16, PQNbits: 8, Seed: 3})
	if idx.codec == nil {
		t.Fatal("IVFADC should have trained a residual codec")
	}
	flat := buildFlat(t, vecs, distance.L2Squared)

	queries := randomVectors(80, dim, 88)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		// UseRerank pulls in the colocated full-precision vectors to correct ADC.
		ir, _ := idx.Search(ctx, q, k, nil, SearchParams{NProbe: 24, UseRerank: true, RerankFactor: 8})
		fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
		total += recallAt(ir, fr, k)
	}
	if avg := total / float64(len(queries)); avg < 0.80 {
		t.Fatalf("IVFADC recall@%d = %.3f, want >= 0.80", k, avg)
	}
}

func TestIVFOPQResidual(t *testing.T) {
	const n, dim = 2500, 64
	vecs := randomVectors(n, dim, 17)
	idx := buildIVF(t, vecs, IVFConfig{Metric: distance.L2Squared, NList: 32, PQM: 8, UseOPQ: true, Seed: 9})
	if idx.codec == nil {
		t.Fatal("OPQ IVFADC should have trained a residual codec")
	}
	// A query equal to a stored vector must return that position with rerank.
	r, _ := idx.Search(context.Background(), vecs[100], 5, nil, SearchParams{NProbe: 16, UseRerank: true, RerankFactor: 8})
	found := false
	for _, c := range r {
		if c.Position == 100 {
			found = true
		}
	}
	if !found {
		t.Fatalf("OPQ IVFADC did not return the exact-match position, got %v", r)
	}
}

func TestIVFCosineFullPrecision(t *testing.T) {
	const n, dim, k = 2000, 48, 10
	vecs := randomVectors(n, dim, 11)
	// Cosine must ignore PQM and keep full-precision entries (spec 08 §17.3).
	idx := buildIVF(t, vecs, IVFConfig{Metric: distance.Cosine, NList: 32, NProbe: 28, PQM: 8, Seed: 5})
	if idx.codec != nil {
		t.Fatal("cosine IVF must not train a residual codec")
	}
	flat := buildFlat(t, vecs, distance.Cosine)
	queries := randomVectors(50, dim, 66)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		ir, _ := idx.Search(ctx, q, k, nil, SearchParams{NProbe: 28})
		fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
		total += recallAt(ir, fr, k)
	}
	if avg := total / float64(len(queries)); avg < 0.88 {
		t.Fatalf("cosine IVF recall@%d = %.3f, want >= 0.88", k, avg)
	}
}

func TestIVFDeterminism(t *testing.T) {
	const n, dim = 2000, 32
	vecs := randomVectors(n, dim, 5)
	cfg := IVFConfig{Metric: distance.L2Squared, NList: 32, Seed: 123}
	a := buildIVF(t, vecs, cfg)
	b := buildIVF(t, vecs, cfg)
	if len(a.centroids) != len(b.centroids) {
		t.Fatal("centroid length mismatch")
	}
	for i := range a.centroids {
		if a.centroids[i] != b.centroids[i] {
			t.Fatalf("centroid %d differs: %f != %f", i, a.centroids[i], b.centroids[i])
		}
	}
}

func TestIVFFilterAndTombstone(t *testing.T) {
	const n, dim, k = 3000, 32, 10
	vecs := randomVectors(n, dim, 13)
	idx := buildIVF(t, vecs, IVFConfig{Metric: distance.L2Squared, NList: 32, NProbe: 32, Seed: 2})
	ctx := context.Background()

	// Filter: only every 5th position is allowed.
	allowed := make([]uint32, 0, n/5)
	for i := 0; i < n; i += 5 {
		allowed = append(allowed, uint32(i))
	}
	filter := NewSliceBitmap(allowed)
	q := randomVectors(1, dim, 555)[0]
	r, _ := idx.Search(ctx, q, k, filter, SearchParams{NProbe: 32})
	for _, c := range r {
		if !filter.Contains(c.Position) {
			t.Fatalf("filtered IVF returned disallowed pos %d", c.Position)
		}
	}

	// Tombstone the nearest result and confirm it disappears.
	r2, _ := idx.Search(ctx, vecs[7], k, nil, SearchParams{NProbe: 32})
	if len(r2) == 0 || r2[0].Position != 7 {
		t.Fatalf("expected pos 7 nearest, got %v", r2[:1])
	}
	if err := idx.Delete(7); err != nil {
		t.Fatal(err)
	}
	r3, _ := idx.Search(ctx, vecs[7], k, nil, SearchParams{NProbe: 32})
	for _, c := range r3 {
		if c.Position == 7 {
			t.Fatal("deleted pos 7 still returned by IVF")
		}
	}
}

func TestIVFPersistRecover(t *testing.T) {
	const n, dim, k = 2000, 32, 10
	vecs := randomVectors(n, dim, 31)
	idx := buildIVF(t, vecs, IVFConfig{Metric: distance.L2Squared, NList: 32, NProbe: 16, PQM: 8, Seed: 5})
	ctx := context.Background()

	store := &memPageStore{}
	if err := idx.Persist(store); err != nil {
		t.Fatal(err)
	}
	idx2, _ := NewIVF(IVFConfig{Dim: dim, Metric: distance.L2Squared})
	if err := idx2.Recover(store); err != nil {
		t.Fatal(err)
	}
	queries := randomVectors(20, dim, 41)
	for _, q := range queries {
		r1, _ := idx.Search(ctx, q, k, nil, SearchParams{NProbe: 16, UseRerank: true})
		r2, _ := idx2.Search(ctx, q, k, nil, SearchParams{NProbe: 16, UseRerank: true})
		if len(r1) != len(r2) {
			t.Fatalf("result count %d != %d after recover", len(r1), len(r2))
		}
		for i := range r1 {
			if r1[i].Position != r2[i].Position {
				t.Fatalf("recovered IVF search differs at %d: %d != %d", i, r1[i].Position, r2[i].Position)
			}
		}
	}
}

func TestIVFCorruptRecover(t *testing.T) {
	idx, _ := NewIVF(IVFConfig{Dim: 8, Metric: distance.L2Squared})
	if err := idx.Recover(&memPageStore{blob: []byte{9, 9, 9}}); err != ErrIndexCorrupt {
		t.Fatalf("err = %v, want ErrIndexCorrupt", err)
	}
}

// buildVamana constructs and builds a Vamana graph over vecs.
func buildVamana(t *testing.T, vecs [][]float32, cfg VamanaConfig) *Vamana {
	t.Helper()
	cfg.Dim = len(vecs[0])
	g, err := NewVamana(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Build(context.Background(), positionsFor(len(vecs)), vectorAtFn(vecs), BuildParams{Metric: cfg.Metric, Seed: cfg.Seed}); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestVamanaRecall(t *testing.T) {
	const n, dim, k = 3000, 64, 10
	vecs := randomVectors(n, dim, 42)
	g := buildVamana(t, vecs, VamanaConfig{Metric: distance.L2Squared, R: 48, L: 100, Seed: 7})
	flat := buildFlat(t, vecs, distance.L2Squared)

	queries := randomVectors(100, dim, 99)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		gr, err := g.Search(ctx, q, k, nil, SearchParams{BeamWidth: 64})
		if err != nil {
			t.Fatal(err)
		}
		fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
		total += recallAt(gr, fr, k)
	}
	if avg := total / float64(len(queries)); avg < 0.90 {
		t.Fatalf("Vamana recall@%d = %.3f, want >= 0.90", k, avg)
	}
}

func TestVamanaExactMatch(t *testing.T) {
	const n, dim = 1500, 32
	vecs := randomVectors(n, dim, 8)
	g := buildVamana(t, vecs, VamanaConfig{Metric: distance.L2Squared, Seed: 4})
	r, _ := g.Search(context.Background(), vecs[100], 5, nil, SearchParams{})
	if len(r) == 0 || r[0].Position != 100 {
		t.Fatalf("Vamana exact-match nearest = %v, want pos 100", r[:1])
	}
}

func TestVamanaDeleteTombstone(t *testing.T) {
	const n, dim, k = 2000, 32, 10
	vecs := randomVectors(n, dim, 8)
	g := buildVamana(t, vecs, VamanaConfig{Metric: distance.L2Squared, Seed: 4})
	ctx := context.Background()
	q := vecs[100]
	before, _ := g.Search(ctx, q, k, nil, SearchParams{})
	if before[0].Position != 100 {
		t.Fatalf("expected pos 100 nearest, got %v", before[:1])
	}
	if err := g.Delete(100); err != nil {
		t.Fatal(err)
	}
	after, _ := g.Search(ctx, q, k, nil, SearchParams{})
	for _, c := range after {
		if c.Position == 100 {
			t.Fatal("deleted pos 100 still returned by Vamana")
		}
	}
	if g.Stats().TombstoneCount != 1 {
		t.Fatalf("tombstone count = %d, want 1", g.Stats().TombstoneCount)
	}
}

func TestVamanaPQNavigation(t *testing.T) {
	const n, dim, k = 3000, 64, 10
	vecs := randomVectors(n, dim, 21)
	codec := trainPQForTest(t, vecs, dim, 16)
	g := buildVamana(t, vecs, VamanaConfig{Metric: distance.L2Squared, R: 48, L: 100, Seed: 7, Codec: codec})
	flat := buildFlat(t, vecs, distance.L2Squared)
	queries := randomVectors(60, dim, 99)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		// PQ navigation finds the neighborhood; colocated full-precision decides.
		gr, _ := g.Search(ctx, q, k, nil, SearchParams{BeamWidth: 96})
		fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
		total += recallAt(gr, fr, k)
	}
	if avg := total / float64(len(queries)); avg < 0.80 {
		t.Fatalf("Vamana PQ-nav recall@%d = %.3f, want >= 0.80", k, avg)
	}
}

func TestVamanaPersistRecover(t *testing.T) {
	const n, dim, k = 2000, 32, 10
	vecs := randomVectors(n, dim, 31)
	g := buildVamana(t, vecs, VamanaConfig{Metric: distance.L2Squared, Seed: 5})
	ctx := context.Background()
	store := &memPageStore{}
	if err := g.Persist(store); err != nil {
		t.Fatal(err)
	}
	g2, _ := NewVamana(VamanaConfig{Dim: dim, Metric: distance.L2Squared})
	if err := g2.Recover(store); err != nil {
		t.Fatal(err)
	}
	queries := randomVectors(20, dim, 41)
	for _, q := range queries {
		r1, _ := g.Search(ctx, q, k, nil, SearchParams{})
		r2, _ := g2.Search(ctx, q, k, nil, SearchParams{})
		if len(r1) != len(r2) {
			t.Fatalf("result count %d != %d after Vamana recover", len(r1), len(r2))
		}
		for i := range r1 {
			if r1[i].Position != r2[i].Position {
				t.Fatalf("recovered Vamana search differs at %d: %d != %d", i, r1[i].Position, r2[i].Position)
			}
		}
	}
}

func TestVamanaCorruptRecover(t *testing.T) {
	g, _ := NewVamana(VamanaConfig{Dim: 8, Metric: distance.L2Squared})
	if err := g.Recover(&memPageStore{blob: []byte{4, 4}}); err != ErrIndexCorrupt {
		t.Fatalf("err = %v, want ErrIndexCorrupt", err)
	}
}

// buildSPANN constructs and builds a SPANN index over vecs.
func buildSPANN(t *testing.T, vecs [][]float32, cfg SPANNConfig) *SPANN {
	t.Helper()
	cfg.Dim = len(vecs[0])
	idx, err := NewSPANN(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Build(context.Background(), positionsFor(len(vecs)), vectorAtFn(vecs), BuildParams{Metric: cfg.Metric, Seed: cfg.Seed}); err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestSPANNRecall(t *testing.T) {
	const n, dim, k = 4000, 64, 10
	vecs := randomVectors(n, dim, 42)
	idx := buildSPANN(t, vecs, SPANNConfig{Metric: distance.L2Squared, NList: 64, NProbe: 40, ReplicaCount: 8, Seed: 7})
	flat := buildFlat(t, vecs, distance.L2Squared)
	queries := randomVectors(100, dim, 99)
	total := 0.0
	ctx := context.Background()
	for _, q := range queries {
		// Boundary replication lifts recall at a given nprobe over plain IVF, but
		// reaching the flat oracle still needs a healthy probe count (spec 08 §10.6).
		sr, err := idx.Search(ctx, q, k, nil, SearchParams{NProbe: 40})
		if err != nil {
			t.Fatal(err)
		}
		fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
		total += recallAt(sr, fr, k)
	}
	if avg := total / float64(len(queries)); avg < 0.90 {
		t.Fatalf("SPANN recall@%d = %.3f, want >= 0.90", k, avg)
	}
}

func TestSPANNBoundaryReplicationHelps(t *testing.T) {
	const n, dim, k = 4000, 64, 10
	vecs := randomVectors(n, dim, 42)
	flat := buildFlat(t, vecs, distance.L2Squared)
	queries := randomVectors(60, dim, 99)
	ctx := context.Background()
	recallFor := func(replicas int, eps float64) float64 {
		idx := buildSPANN(t, vecs, SPANNConfig{Metric: distance.L2Squared, NList: 64, NProbe: 8, ReplicaCount: replicas, BoundaryEps: eps, Seed: 7})
		total := 0.0
		for _, q := range queries {
			sr, _ := idx.Search(ctx, q, k, nil, SearchParams{NProbe: 8})
			fr, _ := flat.Search(ctx, q, k, nil, SearchParams{})
			total += recallAt(sr, fr, k)
		}
		return total / float64(len(queries))
	}
	// At a fixed low nprobe, replicating boundary points into neighbor lists should
	// not reduce recall versus assigning each point to a single list (spec 08 §10.4).
	single := recallFor(1, 1.0)
	replicated := recallFor(8, 1.3)
	if replicated < single {
		t.Fatalf("boundary replication hurt recall: single %.3f, replicated %.3f", single, replicated)
	}
}

func TestSPANNFilterTombstone(t *testing.T) {
	const n, dim, k = 3000, 32, 10
	vecs := randomVectors(n, dim, 13)
	idx := buildSPANN(t, vecs, SPANNConfig{Metric: distance.L2Squared, NList: 32, NProbe: 32, Seed: 2})
	ctx := context.Background()

	allowed := make([]uint32, 0, n/5)
	for i := 0; i < n; i += 5 {
		allowed = append(allowed, uint32(i))
	}
	filter := NewSliceBitmap(allowed)
	q := randomVectors(1, dim, 555)[0]
	r, _ := idx.Search(ctx, q, k, filter, SearchParams{NProbe: 32})
	for _, c := range r {
		if !filter.Contains(c.Position) {
			t.Fatalf("filtered SPANN returned disallowed pos %d", c.Position)
		}
	}

	r2, _ := idx.Search(ctx, vecs[7], k, nil, SearchParams{NProbe: 32})
	if len(r2) == 0 || r2[0].Position != 7 {
		t.Fatalf("expected pos 7 nearest, got %v", r2[:1])
	}
	if err := idx.Delete(7); err != nil {
		t.Fatal(err)
	}
	r3, _ := idx.Search(ctx, vecs[7], k, nil, SearchParams{NProbe: 32})
	for _, c := range r3 {
		if c.Position == 7 {
			t.Fatal("deleted pos 7 still returned by SPANN")
		}
	}
}

func TestSPANNPersistRecover(t *testing.T) {
	const n, dim, k = 2000, 32, 10
	vecs := randomVectors(n, dim, 31)
	idx := buildSPANN(t, vecs, SPANNConfig{Metric: distance.L2Squared, NList: 32, NProbe: 16, Seed: 5})
	ctx := context.Background()
	store := &memPageStore{}
	if err := idx.Persist(store); err != nil {
		t.Fatal(err)
	}
	idx2, _ := NewSPANN(SPANNConfig{Dim: dim, Metric: distance.L2Squared})
	if err := idx2.Recover(store); err != nil {
		t.Fatal(err)
	}
	queries := randomVectors(20, dim, 41)
	for _, q := range queries {
		r1, _ := idx.Search(ctx, q, k, nil, SearchParams{NProbe: 16})
		r2, _ := idx2.Search(ctx, q, k, nil, SearchParams{NProbe: 16})
		if len(r1) != len(r2) {
			t.Fatalf("result count %d != %d after SPANN recover", len(r1), len(r2))
		}
		for i := range r1 {
			if r1[i].Position != r2[i].Position {
				t.Fatalf("recovered SPANN search differs at %d: %d != %d", i, r1[i].Position, r2[i].Position)
			}
		}
	}
}

func TestSPANNCorruptRecover(t *testing.T) {
	idx, _ := NewSPANN(SPANNConfig{Dim: 8, Metric: distance.L2Squared})
	if err := idx.Recover(&memPageStore{blob: []byte{2, 2, 2}}); err != ErrIndexCorrupt {
		t.Fatalf("err = %v, want ErrIndexCorrupt", err)
	}
}
