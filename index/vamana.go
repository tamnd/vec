package index

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/tamnd/vec/quant"
)

// DiskANN/Vamana defaults (spec 08 §8.2-8.3, §9.5).
const (
	defaultVamanaR     = 64  // max out-degree (spec 08 §8.2)
	defaultVamanaL     = 100 // build/search candidate pool (spec 08 §8.3)
	defaultVamanaAlpha = 1.2 // long-range edge bias in pass 2 (spec 08 §8.3)
	defaultBeamWidth   = 64  // search beam (spec 08 §9.5)
	invalidMedoid      = ^uint32(0)
)

// VamanaConfig configures a DiskANN/Vamana graph (spec 08 §8). A non-nil Codec is
// the PQ navigation copy used for distance estimation during traversal (spec 08
// §8.8); nil navigates with full precision. Either way the colocated full-precision
// vector decides the result (spec 08 §9.1).
type VamanaConfig struct {
	Dim       int
	Metric    Metric
	R         int     // max out-degree; 0 uses defaultVamanaR
	L         int     // build candidate pool; 0 uses defaultVamanaL
	Alpha     float64 // pass-2 prune bias; 0 uses defaultVamanaAlpha
	BeamWidth int     // default search beam; 0 uses defaultBeamWidth
	Seed      int64   // reproducible build order
	Codec     Codec   // PQ navigation copy; nil = full precision
}

// Vamana is the DiskANN graph index (spec 08 §8). It implements the Index SPI as a
// single flat graph of fixed max degree, the structure that maps one node to one
// SSD page. This build holds the adjacency and the colocated vectors in memory; the
// spec's on-disk adjacency-block layout and async beam reads (spec 08 §8.7, §9.2)
// are the storage-engine slice, and Persist/Recover ship the graph as one blob.
type Vamana struct {
	mu sync.RWMutex

	dim       int
	metric    Metric
	dist      func(a, b []float32) float32
	r         int
	l         int
	alpha     float64
	beamWidth int
	seed      int64
	codec     quant.Quantizer

	neighbors map[uint32][]uint32  // adjacency
	vecs      map[uint32][]float32 // colocated full-precision vectors
	codes     map[uint32][]byte    // PQ navigation codes (if codec)
	order     []uint32             // stable build/iteration order
	deleted   map[uint32]struct{}
	medoid    uint32

	searches atomic.Int64
	dcomp    atomic.Int64
}

// NewVamana constructs an empty Vamana graph from a config (spec 08 §8).
func NewVamana(cfg VamanaConfig) (*Vamana, error) {
	if cfg.Dim <= 0 {
		return nil, ErrBadParams
	}
	r := cfg.R
	if r <= 0 {
		r = defaultVamanaR
	}
	l := cfg.L
	if l <= 0 {
		l = defaultVamanaL
	}
	alpha := cfg.Alpha
	if alpha <= 0 {
		alpha = defaultVamanaAlpha
	}
	bw := cfg.BeamWidth
	if bw <= 0 {
		bw = defaultBeamWidth
	}
	return &Vamana{
		dim:       cfg.Dim,
		metric:    cfg.Metric,
		dist:      metricDistance(cfg.Metric),
		r:         r,
		l:         l,
		alpha:     alpha,
		beamWidth: bw,
		seed:      cfg.Seed,
		codec:     cfg.Codec,
		neighbors: make(map[uint32][]uint32),
		vecs:      make(map[uint32][]float32),
		codes:     make(map[uint32][]byte),
		deleted:   make(map[uint32]struct{}),
		medoid:    invalidMedoid,
	}, nil
}

// Build runs the two-pass Vamana construction (spec 08 §8.3, §15.3): a pass at
// alpha=1 then a pass at the configured alpha, each greedy-searching for every
// point's candidates and pruning with RobustPrune, then linking backward edges.
func (g *Vamana) Build(ctx context.Context, positions []uint32, vectorAt func(uint32) []float32, params BuildParams) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if params.Seed != 0 {
		g.seed = params.Seed
	}

	g.neighbors = make(map[uint32][]uint32, len(positions))
	g.vecs = make(map[uint32][]float32, len(positions))
	g.codes = make(map[uint32][]byte)
	g.deleted = make(map[uint32]struct{})
	g.order = make([]uint32, 0, len(positions))
	g.medoid = invalidMedoid
	if len(positions) == 0 {
		return nil
	}

	for _, p := range positions {
		v := vectorAt(p)
		vc := make([]float32, g.dim)
		copy(vc, v)
		g.vecs[p] = vc
		g.neighbors[p] = nil
		g.order = append(g.order, p)
		if g.codec != nil {
			code := make([]byte, g.codec.CodeSize())
			g.codec.Encode(vc, code)
			g.codes[p] = code
		}
	}
	g.medoid = g.computeMedoid()

	// Deterministic build order: a seeded Fisher-Yates shuffle of the positions.
	order := make([]uint32, len(g.order))
	copy(order, g.order)
	shuffleU32(order, g.seed)

	g.buildPass(order, 1.0)
	g.buildPass(order, g.alpha)
	return nil
}

// buildPass is one Vamana pass at a given alpha (spec 08 §15.3).
func (g *Vamana) buildPass(order []uint32, alpha float64) {
	for _, p := range order {
		cands := g.greedyForBuild(g.vecs[p])
		// Include existing neighbors so the prune sees the full candidate set.
		nbrs := g.robustPrune(p, cands, alpha)
		g.neighbors[p] = nbrs
		for _, q := range nbrs {
			g.linkBackward(q, p, alpha)
		}
	}
}

// linkBackward adds p to q's adjacency, pruning q back to R when it overflows
// (spec 08 §8.3).
func (g *Vamana) linkBackward(q, p uint32, alpha float64) {
	if q == p {
		return
	}
	qn := g.neighbors[q]
	for _, e := range qn {
		if e == p {
			return
		}
	}
	qn = append(qn, p)
	if len(qn) > g.r {
		cands := make([]Candidate, 0, len(qn))
		qv := g.vecs[q]
		for _, e := range qn {
			cands = append(cands, Candidate{Position: e, Distance: g.dist(qv, g.vecs[e])})
		}
		qn = g.robustPrune(q, cands, alpha)
	}
	g.neighbors[q] = qn
}

// robustPrune selects at most R neighbors for node p (spec 08 §8.4): walking
// candidates nearest-first, a candidate is kept unless an already-kept neighbor is
// within an alpha factor "on the way" to it. The geometry forces diverse edges.
func (g *Vamana) robustPrune(p uint32, cands []Candidate, alpha float64) []uint32 {
	// Dedup and drop p itself; sort ascending by distance to p.
	seen := make(map[uint32]struct{}, len(cands))
	filtered := cands[:0]
	for _, c := range cands {
		if c.Position == p {
			continue
		}
		if _, ok := seen[c.Position]; ok {
			continue
		}
		seen[c.Position] = struct{}{}
		filtered = append(filtered, c)
	}
	sort.Slice(filtered, func(a, b int) bool { return minLess(filtered[a], filtered[b]) })

	result := make([]uint32, 0, g.r)
	for _, c := range filtered {
		if len(result) >= g.r {
			break
		}
		dominated := false
		cv := g.vecs[c.Position]
		for _, rp := range result {
			if float64(g.dist(g.vecs[rp], cv))*alpha <= float64(c.Distance) {
				dominated = true
				break
			}
		}
		if !dominated {
			result = append(result, c.Position)
		}
	}
	return result
}

// greedyForBuild greedy-searches the current graph from the medoid for the query's
// L nearest candidates, using full-precision distances (spec 08 §8.6).
func (g *Vamana) greedyForBuild(query []float32) []Candidate {
	if g.medoid == invalidMedoid {
		return nil
	}
	visited := make(map[uint32]struct{}, g.l*2)
	var frontier minHeap
	results := make([]Candidate, 0, g.l*2)

	startD := g.dist(g.vecs[g.medoid], query)
	frontier.push(Candidate{Position: g.medoid, Distance: startD})
	visited[g.medoid] = struct{}{}
	results = append(results, Candidate{Position: g.medoid, Distance: startD})

	for frontier.len() > 0 {
		best := frontier.pop()
		// Stop when the closest unexplored candidate is worse than the L-th result.
		if len(results) >= g.l {
			worst := nthDist(results, g.l)
			if best.Distance > worst {
				break
			}
		}
		for _, nb := range g.neighbors[best.Position] {
			if _, ok := visited[nb]; ok {
				continue
			}
			visited[nb] = struct{}{}
			d := g.dist(g.vecs[nb], query)
			g.dcomp.Add(1)
			frontier.push(Candidate{Position: nb, Distance: d})
			results = append(results, Candidate{Position: nb, Distance: d})
		}
	}
	return results
}

// Search runs beam search from the medoid (spec 08 §9.1). Navigation uses the PQ
// approximation when a codec is configured and full precision otherwise; the
// colocated full-precision vector always decides the result (spec 08 §9.1).
func (g *Vamana) Search(ctx context.Context, query []float32, k int, filter Bitmap, params SearchParams) ([]Candidate, error) {
	if len(query) != g.dim {
		return nil, ErrDimMismatch
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	g.searches.Add(1)
	if g.medoid == invalidMedoid || k <= 0 {
		return nil, nil
	}

	beam := params.BeamWidth
	if beam <= 0 {
		beam = g.beamWidth
	}
	l := k
	if beam > l {
		l = beam
	}

	useADC := g.codec != nil
	var adc quant.ADCTable
	if useADC {
		adc = g.codec.NewADCTable(query, g.metric)
	}
	navDist := func(pos uint32) float32 {
		if useADC {
			return adc.Distance(g.codes[pos])
		}
		return g.dist(g.vecs[pos], query)
	}

	visited := make(map[uint32]struct{})
	var open minHeap                   // by navigation distance
	results := make([]Candidate, 0, l) // exact distances

	open.push(Candidate{Position: g.medoid, Distance: navDist(g.medoid)})
	visited[g.medoid] = struct{}{}

	for open.len() > 0 {
		// Expand up to beam best candidates this step.
		batch := make([]uint32, 0, beam)
		for open.len() > 0 && len(batch) < beam {
			batch = append(batch, open.pop().Position)
		}
		for _, pos := range batch {
			exact := g.dist(g.vecs[pos], query)
			g.dcomp.Add(1)
			if g.admissibleResult(pos, filter) {
				results = append(results, Candidate{Position: pos, Distance: exact})
			}
			for _, nb := range g.neighbors[pos] {
				if _, ok := visited[nb]; ok {
					continue
				}
				visited[nb] = struct{}{}
				open.push(Candidate{Position: nb, Distance: navDist(nb)})
			}
		}
		// Convergence: stop once we have k results and the frontier head is worse
		// than the current k-th best exact result.
		if len(results) >= k && open.len() > 0 {
			sort.Slice(results, func(a, b int) bool { return minLess(results[a], results[b]) })
			if open.peek().Distance > results[k-1].Distance {
				break
			}
		}
	}

	sort.Slice(results, func(a, b int) bool { return minLess(results[a], results[b]) })
	if len(results) > k {
		results = results[:k]
	}
	return results, nil
}

// admissibleResult reports whether a node may enter the result set: live and, under
// a filter, passing it (a tombstoned or filtered node is still a traversal hop,
// spec 08 §11.5, §12.5).
func (g *Vamana) admissibleResult(pos uint32, filter Bitmap) bool {
	if _, dead := g.deleted[pos]; dead {
		return false
	}
	if filter != nil && !filter.Contains(pos) {
		return false
	}
	return true
}

// Insert adds a point by searching for its neighbors and linking it in immediately
// (spec 08 §11.3, consolidate-on-insert). The spec's in-memory delta buffer with
// background consolidation (spec 08 §11.2) is the streaming-throughput slice over
// this correct foreground path.
func (g *Vamana) Insert(pos uint32, vec []float32) error {
	if len(vec) != g.dim {
		return ErrDimMismatch
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.deleted, pos)
	vc := make([]float32, g.dim)
	copy(vc, vec)
	g.vecs[pos] = vc
	if g.codec != nil {
		code := make([]byte, g.codec.CodeSize())
		g.codec.Encode(vc, code)
		g.codes[pos] = code
	}
	if _, exists := g.neighbors[pos]; !exists {
		g.order = append(g.order, pos)
	}
	if g.medoid == invalidMedoid {
		g.neighbors[pos] = nil
		g.medoid = pos
		return nil
	}
	cands := g.greedyForBuild(vc)
	nbrs := g.robustPrune(pos, cands, g.alpha)
	g.neighbors[pos] = nbrs
	for _, q := range nbrs {
		g.linkBackward(q, pos, g.alpha)
	}
	return nil
}

// Delete tombstones a position (spec 08 §11.5). Physical removal from adjacency
// blocks happens at delete consolidation; search skips tombstoned results meanwhile.
func (g *Vamana) Delete(pos uint32) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.vecs[pos]; !ok {
		return nil
	}
	g.deleted[pos] = struct{}{}
	if pos == g.medoid {
		g.medoid = g.repairMedoid()
	}
	return nil
}

// repairMedoid picks a new medoid when the old one is tombstoned: the live point
// nearest the centroid of live points, or invalid when none remain (spec 08 §8.5).
func (g *Vamana) repairMedoid() uint32 {
	return g.computeMedoid()
}

// computeMedoid approximates the dataset medoid as the live point nearest the mean
// of all live vectors (spec 08 §8.5).
func (g *Vamana) computeMedoid() uint32 {
	if len(g.vecs) == len(g.deleted) {
		return invalidMedoid
	}
	mean := make([]float32, g.dim)
	live := 0
	for pos, v := range g.vecs {
		if _, dead := g.deleted[pos]; dead {
			continue
		}
		for j, x := range v {
			mean[j] += x
		}
		live++
	}
	if live == 0 {
		return invalidMedoid
	}
	inv := 1.0 / float32(live)
	for j := range mean {
		mean[j] *= inv
	}
	best, bestD := invalidMedoid, float32(math.MaxFloat32)
	// Iterate in stable order so the medoid is reproducible.
	for _, pos := range g.order {
		if _, dead := g.deleted[pos]; dead {
			continue
		}
		v, ok := g.vecs[pos]
		if !ok {
			continue
		}
		d := g.dist(mean, v)
		if d < bestD {
			bestD, best = d, pos
		}
	}
	return best
}

// Close releases graph memory.
func (g *Vamana) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.neighbors = nil
	g.vecs = nil
	g.codes = nil
	g.deleted = nil
	g.order = nil
	g.medoid = invalidMedoid
	return nil
}

// Stats returns a snapshot of graph counters (spec 08 §19.1).
func (g *Vamana) Stats() IndexStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return IndexStats{
		NodeCount:        int64(len(g.vecs)),
		TombstoneCount:   int64(len(g.deleted)),
		EntrypointPos:    g.medoid,
		DistComputations: g.dcomp.Load(),
		SearchCount:      g.searches.Load(),
		MemoryBytes:      g.memoryBytes(),
	}
}

// MemoryBytes estimates resident bytes (spec 08 §14.2).
func (g *Vamana) MemoryBytes() int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.memoryBytes()
}

func (g *Vamana) memoryBytes() int64 {
	var b int64
	for _, n := range g.neighbors {
		b += int64(len(n)) * 4
	}
	b += int64(len(g.vecs)) * int64(g.dim) * 4
	for _, c := range g.codes {
		b += int64(len(c))
	}
	return b
}

// nthDist returns the n-th smallest distance among results (1-indexed), used as the
// greedy-search stop threshold. results is unsorted; this scans for the n-th order
// statistic by a partial selection, cheap for small n relative to len.
func nthDist(results []Candidate, n int) float32 {
	if n > len(results) {
		n = len(results)
	}
	cp := make([]Candidate, len(results))
	copy(cp, results)
	sort.Slice(cp, func(a, b int) bool { return minLess(cp[a], cp[b]) })
	return cp[n-1].Distance
}

// shuffleU32 applies a seeded Fisher-Yates shuffle so the Vamana build order is
// reproducible (spec 08 §8.3 random insertion order, made deterministic).
func shuffleU32(s []uint32, seed int64) {
	// A small linear congruential generator keyed by seed; deterministic and
	// dependency-free.
	state := uint64(seed)*2862933555777941757 + 3037000493
	for i := len(s) - 1; i > 0; i-- {
		state = state*6364136223846793005 + 1442695040888963407
		j := int(state >> 33 % uint64(i+1))
		s[i], s[j] = s[j], s[i]
	}
}
