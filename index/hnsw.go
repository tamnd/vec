package index

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
)

// invalidPos marks an absent entrypoint on an empty (or fully tombstoned) graph
// (spec 07 §21.1).
const invalidPos = ^uint32(0)

// defaultLevelSeed seeds level draws when the caller passes Seed=0, keeping Build
// reproducible within a database rather than truly random (spec 07 §15.3). A fresh
// random seed per process would break the determinism tests; the spec stores the
// seed in the header so a reopen reuses it, which a fixed default models.
const defaultLevelSeed = 0x48_4E_53_57 // "HNSW"

// hnswNode is one graph node held on the Go heap. The spec's mmap off-GC layout
// (spec 07 §8.2-8.3) is wired in the storage-engine slice; the in-memory shape and
// every algorithm are identical.
type hnswNode struct {
	maxLevel  int
	neighbors [][]uint32 // [layer] -> neighbor positions; len == maxLevel+1
	createdAt uint64     // txn sequence that inserted this node (MVCC, spec 07 §12.3)
	deletedAt uint64     // txn sequence that tombstoned it; 0 = live
}

// HNSW is vec's default ANN index (spec 07 §17.1). Insert and Delete are serialized
// by the outer write lock; Search takes the read lock, so concurrent searches run
// together and exclude only an in-flight mutation. Per-node fine-grained locking
// (spec 07 §12.1) is a throughput slice over this correct baseline.
type HNSW struct {
	mu sync.RWMutex

	m              int
	m0             int
	mL             float64
	efConstruction int
	metric         Metric
	dim            int
	codec          Codec
	naiveSelect    bool
	seed           int64

	dist func(a, b []float32) float32

	nodes map[uint32]*hnswNode
	vecs  map[uint32][]float32 // full-precision vectors, by position
	codes map[uint32][]byte    // navigation codes when codec != nil

	entrypoint uint32
	maxLayer   int
	rng        *rand.Rand

	closed bool

	searches atomic.Int64
	dcomp    atomic.Int64
}

// HNSWConfig configures a new index (spec 07 §17.3).
type HNSWConfig struct {
	Dim            int
	Metric         Metric
	M              int
	M0             int
	EfConstruction int
	ML             float64
	Seed           int64
	Codec          Codec
	NaiveSelect    bool
}

// NewHNSW returns an empty HNSW index ready for Insert or Build (spec 07 §17.2).
func NewHNSW(cfg HNSWConfig) (*HNSW, error) {
	if cfg.Dim <= 0 {
		return nil, ErrBadParams
	}
	m := cfg.M
	if m <= 0 {
		m = 16
	}
	m0 := cfg.M0
	if m0 <= 0 {
		m0 = 2 * m
	}
	efc := cfg.EfConstruction
	if efc <= 0 {
		efc = 200
	}
	if efc < m {
		return nil, ErrBadParams
	}
	mL := cfg.ML
	if mL <= 0 {
		mL = 1.0 / math.Log(float64(m))
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = defaultLevelSeed
	}
	h := &HNSW{
		m:              m,
		m0:             m0,
		mL:             mL,
		efConstruction: efc,
		metric:         cfg.Metric,
		dim:            cfg.Dim,
		codec:          cfg.Codec,
		naiveSelect:    cfg.NaiveSelect,
		seed:           seed,
		dist:           metricDistance(cfg.Metric),
		nodes:          make(map[uint32]*hnswNode),
		vecs:           make(map[uint32][]float32),
		codes:          make(map[uint32][]byte),
		entrypoint:     invalidPos,
		maxLayer:       -1,
		rng:            rand.New(rand.NewSource(seed)),
	}
	return h, nil
}

// ensureRNG lazily restores the level-draw RNG (e.g. after Recover, which does
// not persist RNG state). The caller holds the write lock.
func (h *HNSW) ensureRNG() {
	if h.rng == nil {
		h.rng = rand.New(rand.NewSource(h.seed))
	}
}

// drawLevel draws a node's maximum level from the geometric distribution
// (spec 07 §5.2).
func (h *HNSW) drawLevel(rng *rand.Rand) int {
	f := -math.Log(rng.Float64()) * h.mL
	l := int(math.Floor(f))
	if l < 0 {
		l = 0
	}
	return l
}

// vectorAt returns the stored full-precision vector for pos.
func (h *HNSW) vectorAt(pos uint32) []float32 { return h.vecs[pos] }

// navDist computes the navigation distance from query to pos: full precision when
// there is no codec, otherwise the decoded-approximate distance over the stored
// code (spec 07 §10.3). The final ranking is corrected by rerank (spec 07 §10.4).
func (h *HNSW) navDist(query, decodeBuf []float32, pos uint32) float32 {
	h.dcomp.Add(1)
	if h.codec == nil {
		return h.dist(query, h.vecs[pos])
	}
	h.codec.Decode(h.codes[pos], decodeBuf)
	return h.dist(query, decodeBuf)
}

// storeVector records pos's vector (a private copy) and, if a codec is set, its
// navigation code.
func (h *HNSW) storeVector(pos uint32, vec []float32) {
	cp := make([]float32, len(vec))
	copy(cp, vec)
	h.vecs[pos] = cp
	if h.codec != nil {
		code := make([]byte, h.codec.CodeSize())
		h.codec.Encode(cp, code)
		h.codes[pos] = code
	}
}

// Build constructs the graph from scratch by inserting every position in order
// with deterministic, seeded level draws (spec 07 §5.6, §14.4).
func (h *HNSW) Build(ctx context.Context, positions []uint32, vectorAt func(uint32) []float32, params BuildParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	h.applyBuildParams(params)

	// Reset graph state.
	h.nodes = make(map[uint32]*hnswNode, len(positions))
	h.vecs = make(map[uint32][]float32, len(positions))
	h.codes = make(map[uint32][]byte)
	h.entrypoint = invalidPos
	h.maxLayer = -1
	h.rng = rand.New(rand.NewSource(h.seed))

	// Draw all levels up front in position order so the build is reproducible
	// regardless of how insertion is later parallelized (spec 07 §5.6).
	for _, pos := range positions {
		if err := ctx.Err(); err != nil {
			return err
		}
		v := vectorAt(pos)
		if len(v) != h.dim {
			return ErrDimMismatch
		}
		h.storeVector(pos, v)
		level := h.drawLevel(h.rng)
		h.insertLocked(pos, level)
	}
	return nil
}

// applyBuildParams folds non-zero BuildParams over the current configuration.
func (h *HNSW) applyBuildParams(p BuildParams) {
	if p.M > 0 {
		h.m = p.M
	}
	if p.M0 > 0 {
		h.m0 = p.M0
	} else if p.M > 0 {
		h.m0 = 2 * p.M
	}
	if p.EfConstruction > 0 {
		h.efConstruction = p.EfConstruction
	}
	if p.ML > 0 {
		h.mL = p.ML
	}
	if p.Seed != 0 {
		h.seed = p.Seed
	}
	if p.Codec != nil {
		h.codec = p.Codec
	}
	if p.Metric != 0 {
		h.metric = p.Metric
		h.dist = metricDistance(p.Metric)
	}
	h.naiveSelect = p.NaiveSelect
}

// Insert adds a single point (spec 07 §5.4). It is serialized by the write lock.
func (h *HNSW) Insert(pos uint32, vec []float32) error {
	if len(vec) != h.dim {
		return ErrDimMismatch
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	h.ensureRNG()
	h.storeVector(pos, vec)
	h.insertLocked(pos, h.drawLevel(h.rng))
	return nil
}

// insertLocked links pos into the graph at the given level. The caller holds the
// write lock (spec 07 §5.4).
func (h *HNSW) insertLocked(pos uint32, level int) {
	if existing, ok := h.nodes[pos]; ok {
		// Re-insert of a tombstoned position: revive it and relink fresh.
		existing.deletedAt = 0
	}
	node := &hnswNode{maxLevel: level, neighbors: make([][]uint32, level+1)}
	h.nodes[pos] = node
	vec := h.vecs[pos]
	decodeBuf := h.decodeBuffer()

	// First node becomes the entrypoint.
	if h.entrypoint == invalidPos {
		h.entrypoint = pos
		h.maxLayer = level
		return
	}

	cur := h.entrypoint
	curDist := h.navDist(vec, decodeBuf, cur)

	// Descend the layers above this node's top level to find the entry point.
	for layer := h.maxLayer; layer > level; layer-- {
		cur, curDist = h.greedyDescend(vec, decodeBuf, cur, curDist, layer)
	}

	// From this node's top level down to 0, find neighbors and link both ways.
	for layer := min(level, h.maxLayer); layer >= 0; layer-- {
		maxM := h.m
		if layer == 0 {
			maxM = h.m0
		}
		candidates := h.searchLayer(vec, decodeBuf, cur, h.efConstruction, layer, nil)
		neighbors := h.selectNeighbors(vec, candidates, maxM, layer)
		node.neighbors[layer] = neighbors
		for _, nb := range neighbors {
			h.linkBack(nb, pos, layer, maxM)
		}
		if len(candidates) > 0 {
			// Thread the nearest candidate as the entry point for the next layer
			// down. Only the position carries over; the lower layer recomputes
			// distances inside searchLayer.
			cur = candidates[0].Position
		}
	}

	if level > h.maxLayer {
		h.maxLayer = level
		h.entrypoint = pos
	}
}

// linkBack adds src to dst's neighbor list at the layer, pruning back to maxM with
// the heuristic if the list overflows (spec 07 §5.5).
func (h *HNSW) linkBack(dst, src uint32, layer, maxM int) {
	node := h.nodes[dst]
	if node == nil || layer > node.maxLevel {
		return
	}
	existing := node.neighbors[layer]
	for _, p := range existing {
		if p == src {
			return // already linked
		}
	}
	existing = append(existing, src)
	if len(existing) > maxM {
		existing = h.pruneNeighbors(dst, existing, maxM)
	}
	node.neighbors[layer] = existing
}

// pruneNeighbors keeps the best maxM neighbors of pos using the same heuristic as
// selection, scored by distance from pos (spec 07 §5.5).
func (h *HNSW) pruneNeighbors(pos uint32, neighbors []uint32, maxM int) []uint32 {
	base := h.vecs[pos]
	cands := make([]Candidate, 0, len(neighbors))
	for _, nb := range neighbors {
		cands = append(cands, Candidate{Position: nb, Distance: h.dist(base, h.vecs[nb])})
	}
	sort.Slice(cands, func(i, j int) bool { return minLess(cands[i], cands[j]) })
	return h.selectNeighbors(base, cands, maxM, 0)
}

// selectNeighbors picks up to maxM diverse neighbors (spec 07 §5.3). The heuristic
// rejects a candidate that is closer to an already-selected neighbor than to the
// new point; keepPruned fills any shortfall with the best rejected candidates.
func (h *HNSW) selectNeighbors(base []float32, candidates []Candidate, maxM, layer int) []uint32 {
	if len(candidates) <= maxM {
		out := make([]uint32, len(candidates))
		for i, c := range candidates {
			out[i] = c.Position
		}
		return out
	}
	if h.naiveSelect {
		out := make([]uint32, maxM)
		for i := 0; i < maxM; i++ {
			out[i] = candidates[i].Position
		}
		return out
	}

	selected := make([]uint32, 0, maxM)
	discarded := make([]uint32, 0)
	for _, e := range candidates { // candidates are sorted ascending by distance
		if len(selected) >= maxM {
			break
		}
		closer := true
		ev := h.vecs[e.Position]
		for _, s := range selected {
			if h.dist(ev, h.vecs[s]) < e.Distance {
				closer = false
				break
			}
		}
		if closer {
			selected = append(selected, e.Position)
		} else {
			discarded = append(discarded, e.Position)
		}
	}
	for i := 0; len(selected) < maxM && i < len(discarded); i++ {
		selected = append(selected, discarded[i])
	}
	return selected
}

// greedyDescend moves to the closest neighbor at the layer until none is closer
// (spec 07 §4.3), returning the closest node and its distance.
func (h *HNSW) greedyDescend(query, decodeBuf []float32, ep uint32, epDist float32, layer int) (uint32, float32) {
	cur, curDist := ep, epDist
	for {
		changed := false
		node := h.nodes[cur]
		if node == nil || layer > node.maxLevel {
			break
		}
		for _, nb := range node.neighbors[layer] {
			d := h.navDist(query, decodeBuf, nb)
			if d < curDist {
				cur, curDist, changed = nb, d, true
			}
		}
		if !changed {
			break
		}
	}
	return cur, curDist
}

// searchLayer runs the ef-bounded best-first search at one layer (spec 07 §4.4).
// Tombstoned nodes still serve as traversal hops but are excluded from results
// (spec 07 §7.2); filter excludes non-matching nodes from results only (in-filter,
// spec 07 §11.1).
func (h *HNSW) searchLayer(query, decodeBuf []float32, ep uint32, ef, layer int, filter Bitmap) []Candidate {
	visited := make(map[uint32]struct{}, ef*2)
	var cands minHeap
	var results maxHeap

	d0 := h.navDist(query, decodeBuf, ep)
	cands.push(Candidate{ep, d0})
	visited[ep] = struct{}{}
	if h.admissible(ep, filter) {
		results.push(Candidate{ep, d0})
	}

	for cands.len() > 0 {
		c := cands.pop()
		if results.len() >= ef && c.Distance > results.peek().Distance {
			break
		}
		node := h.nodes[c.Position]
		if node == nil || layer > node.maxLevel {
			continue
		}
		for _, nb := range node.neighbors[layer] {
			if _, seen := visited[nb]; seen {
				continue
			}
			visited[nb] = struct{}{}
			d := h.navDist(query, decodeBuf, nb)
			if results.len() < ef || d < results.peek().Distance {
				cands.push(Candidate{nb, d})
				if h.admissible(nb, filter) {
					results.push(Candidate{nb, d})
					if results.len() > ef {
						results.pop()
					}
				}
			}
		}
	}

	out := make([]Candidate, results.len())
	copy(out, results)
	sort.Slice(out, func(i, j int) bool { return minLess(out[i], out[j]) })
	return out
}

// admissible reports whether a node may enter the result set: it must be live and
// pass the filter (spec 07 §7.2, §11.1).
func (h *HNSW) admissible(pos uint32, filter Bitmap) bool {
	node := h.nodes[pos]
	if node == nil || node.deletedAt != 0 {
		return false
	}
	return filter == nil || filter.Contains(pos)
}

func (h *HNSW) decodeBuffer() []float32 {
	if h.codec == nil {
		return nil
	}
	return make([]float32, h.dim)
}

// Search returns the k nearest live candidates passing filter (spec 07 §4.5).
func (h *HNSW) Search(ctx context.Context, query []float32, k int, filter Bitmap, params SearchParams) ([]Candidate, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil, ErrClosed
	}
	if len(query) != h.dim {
		return nil, ErrDimMismatch
	}
	h.searches.Add(1)
	if h.entrypoint == invalidPos {
		return nil, nil
	}

	ef := params.EfSearch
	if ef <= 0 {
		ef = 50
	}
	if ef < k {
		ef = k
	}

	decodeBuf := h.decodeBuffer()
	ep := h.entrypoint
	epDist := h.navDist(query, decodeBuf, ep)
	for layer := h.maxLayer; layer >= 1; layer-- {
		ep, epDist = h.greedyDescend(query, decodeBuf, ep, epDist, layer)
	}

	candidates := h.searchLayer(query, decodeBuf, ep, ef, 0, filter)

	rerank := params.UseRerank && h.codec != nil
	if rerank {
		factor := params.RerankFactor
		if factor <= 0 {
			factor = 3
		}
		candidates = h.rerankLocked(query, candidates, factor*k)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates, nil
}

// rerankLocked recomputes distances on the top r candidates with full-precision
// vectors and re-sorts (spec 07 §10.4). The caller holds the read lock.
func (h *HNSW) rerankLocked(query []float32, candidates []Candidate, r int) []Candidate {
	if len(candidates) > r {
		candidates = candidates[:r]
	}
	for i := range candidates {
		candidates[i].Distance = h.dist(query, h.vecs[candidates[i].Position])
	}
	sort.Slice(candidates, func(i, j int) bool { return minLess(candidates[i], candidates[j]) })
	return candidates
}

// Delete tombstones a point (spec 07 §7.2). The node stays in the graph as a
// traversal hop; entrypoint loss is repaired here (spec 07 §7.4).
func (h *HNSW) Delete(pos uint32) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	node := h.nodes[pos]
	if node == nil || node.deletedAt != 0 {
		return nil
	}
	node.deletedAt = 1
	if pos == h.entrypoint {
		h.repairEntrypoint(pos)
	}
	return nil
}

// repairEntrypoint picks a new entrypoint when the current one is tombstoned
// (spec 07 §7.4): the closest live neighbor walking down from the top layer, or a
// scan for the highest-level live node, or invalidPos if the graph is empty.
func (h *HNSW) repairEntrypoint(old uint32) {
	node := h.nodes[old]
	if node != nil {
		for layer := node.maxLevel; layer >= 0; layer-- {
			for _, nb := range node.neighbors[layer] {
				if n := h.nodes[nb]; n != nil && n.deletedAt == 0 {
					h.entrypoint = nb
					h.maxLayer = n.maxLevel
					return
				}
			}
		}
	}
	// Fall back to an O(n) scan for the highest-level live node.
	best := invalidPos
	bestLevel := -1
	for pos, n := range h.nodes {
		if n.deletedAt != 0 {
			continue
		}
		if n.maxLevel > bestLevel || (n.maxLevel == bestLevel && pos < best) {
			best, bestLevel = pos, n.maxLevel
		}
	}
	h.entrypoint = best
	if best == invalidPos {
		h.maxLayer = -1
	} else {
		h.maxLayer = bestLevel
	}
}

// Close releases the index (spec 07 §1.4).
func (h *HNSW) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.nodes = nil
	h.vecs = nil
	h.codes = nil
	return nil
}

// Stats returns a counter snapshot (spec 07 §1.3).
func (h *HNSW) Stats() IndexStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var live, tomb int64
	hist := make([]int64, h.maxLayer+1)
	for _, n := range h.nodes {
		if n.deletedAt != 0 {
			tomb++
			continue
		}
		live++
		for l := 0; l <= n.maxLevel && l < len(hist); l++ {
			hist[l]++
		}
	}
	return IndexStats{
		NodeCount:        live,
		TombstoneCount:   tomb,
		LayerHistogram:   hist,
		EntrypointPos:    h.entrypoint,
		DistComputations: h.dcomp.Load(),
		SearchCount:      h.searches.Load(),
		MemoryBytes:      h.MemoryBytes(),
	}
}

// MemoryBytes estimates resident bytes for the graph and stores (spec 07 §13.1).
func (h *HNSW) MemoryBytes() int64 {
	var b int64
	for _, n := range h.nodes {
		b += 48 // node record + visibility
		for _, layer := range n.neighbors {
			b += int64(len(layer)) * 4
		}
	}
	b += int64(len(h.vecs)) * int64(h.dim*4)
	for _, c := range h.codes {
		b += int64(len(c))
	}
	return b
}

// tombstoneRatio reports tombstones / total (spec 07 §7.3). The caller holds a
// lock.
func (h *HNSW) tombstoneRatio() float64 {
	var live, tomb int
	for _, n := range h.nodes {
		if n.deletedAt != 0 {
			tomb++
		} else {
			live++
		}
	}
	if live+tomb == 0 {
		return 0
	}
	return float64(tomb) / float64(live+tomb)
}
