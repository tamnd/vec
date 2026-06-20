package index

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
)

// SPANN defaults (spec 08 §10.2-10.4).
const (
	defaultSpannReplicas = 8   // max posting lists a boundary point joins (spec 08 §10.4)
	defaultBoundaryEps   = 1.1 // replicate to a list within 1.1x the nearest (spec 08 §10.4)
	defaultSpannKMeansIt = 20
)

// SPANNConfig configures a SPANN index (spec 08 §10): an in-RAM centroid index over
// disk-resident posting lists, with boundary replication so a query that lands near
// a cell edge still finds neighbors that fell into the neighboring cell.
type SPANNConfig struct {
	Dim          int
	Metric       Metric
	NList        int     // number of posting lists (centroids); 0 derives sqrt(n)
	NProbe       int     // posting lists scanned per query; 0 uses defaultNProbe
	ReplicaCount int     // max lists a boundary point joins; 0 uses defaultSpannReplicas
	BoundaryEps  float64 // replication factor; 0 uses defaultBoundaryEps
	Seed         int64
	KMeansIt     int
}

// spannEntry is one posting-list member: a position. The full-precision vector lives
// once in vecs and is referenced by every list the point replicates into.
type spannEntry struct {
	pos uint32
}

// SPANN is the SPANN index (spec 08 §10). The centroid index is an in-memory HNSW
// (spec 08 §10.3) whose positions are centroid ids; posting lists hold member
// positions. The spec's on-disk posting lists with async reads (spec 08 §10.5) are
// the storage-engine slice; this build keeps lists and vectors in memory and ships
// the same boundary-replicated search, with Persist/Recover over a PageStore blob.
type SPANN struct {
	mu sync.RWMutex

	dim          int
	metric       Metric
	dist         func(a, b []float32) float32
	nlist        int
	nprobe       int
	replicaCount int
	boundaryEps  float64
	seed         int64
	kmeansIt     int

	centroidIndex *HNSW
	centroids     []float32 // nlist*dim, row-major
	lists         map[int][]spannEntry
	vecs          map[uint32][]float32
	deleted       map[uint32]struct{}

	searches atomic.Int64
	dcomp    atomic.Int64
}

// NewSPANN constructs an empty SPANN index from a config (spec 08 §10).
func NewSPANN(cfg SPANNConfig) (*SPANN, error) {
	if cfg.Dim <= 0 {
		return nil, ErrBadParams
	}
	nprobe := cfg.NProbe
	if nprobe <= 0 {
		nprobe = defaultNProbe
	}
	replicas := cfg.ReplicaCount
	if replicas <= 0 {
		replicas = defaultSpannReplicas
	}
	eps := cfg.BoundaryEps
	if eps <= 0 {
		eps = defaultBoundaryEps
	}
	it := cfg.KMeansIt
	if it <= 0 {
		it = defaultSpannKMeansIt
	}
	return &SPANN{
		dim:          cfg.Dim,
		metric:       cfg.Metric,
		dist:         metricDistance(cfg.Metric),
		nlist:        cfg.NList,
		nprobe:       nprobe,
		replicaCount: replicas,
		boundaryEps:  eps,
		seed:         cfg.Seed,
		kmeansIt:     it,
		lists:        make(map[int][]spannEntry),
		vecs:         make(map[uint32][]float32),
		deleted:      make(map[uint32]struct{}),
	}, nil
}

// Build trains centroids, builds the in-RAM centroid index over them, then assigns
// every point to its nearest list plus any boundary list within the replication
// factor (spec 08 §10.4, §15.4).
func (s *SPANN) Build(ctx context.Context, positions []uint32, vectorAt func(uint32) []float32, params BuildParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if params.Seed != 0 {
		s.seed = params.Seed
	}

	s.lists = make(map[int][]spannEntry)
	s.vecs = make(map[uint32][]float32, len(positions))
	s.deleted = make(map[uint32]struct{})
	if len(positions) == 0 {
		s.centroids = nil
		s.centroidIndex = nil
		return nil
	}

	n := len(positions)
	for _, p := range positions {
		v := vectorAt(p)
		vc := make([]float32, s.dim)
		copy(vc, v)
		s.vecs[p] = vc
	}

	// Resolve list count: configured, else sqrt(n) rounded to a power of two, keeping
	// a healthy floor of points per list (spec 08 §10.2).
	nlist := s.nlist
	if nlist <= 0 {
		nlist = roundPow2(int(isqrt(n)))
	}
	if nlist < 1 {
		nlist = 1
	}
	if nlist > n {
		nlist = n
	}
	s.nlist = nlist

	// Train centroids on the materialized matrix.
	data := make([]float32, n*s.dim)
	for i, p := range positions {
		copy(data[i*s.dim:(i+1)*s.dim], s.vecs[p])
	}
	s.centroids = trainCentroids(data, n, s.dim, nlist, s.kmeansIt, s.seed)

	// Build the centroid index: an HNSW whose positions are centroid ids.
	ci, err := NewHNSW(HNSWConfig{Dim: s.dim, Metric: s.metric, Seed: s.seed})
	if err != nil {
		return err
	}
	cpos := make([]uint32, nlist)
	for c := 0; c < nlist; c++ {
		cpos[c] = uint32(c)
	}
	centroidAt := func(pos uint32) []float32 {
		return s.centroids[int(pos)*s.dim : (int(pos)+1)*s.dim]
	}
	if err := ci.Build(ctx, cpos, centroidAt, BuildParams{Metric: s.metric, Seed: s.seed}); err != nil {
		return err
	}
	s.centroidIndex = ci

	// Assign each point to its nearest list plus boundary lists (spec 08 §10.4).
	for _, p := range positions {
		s.assignReplicated(p, s.vecs[p])
	}
	return nil
}

// assignReplicated puts a point in its nearest list and every other list whose
// centroid is within boundaryEps of the nearest, up to replicaCount lists
// (spec 08 §10.4). The caller holds the write lock.
func (s *SPANN) assignReplicated(pos uint32, vec []float32) {
	type cd struct {
		c int
		d float32
	}
	ds := make([]cd, s.nlist)
	for c := 0; c < s.nlist; c++ {
		cen := s.centroids[c*s.dim : (c+1)*s.dim]
		ds[c] = cd{c: c, d: s.dist(cen, vec)}
	}
	sort.Slice(ds, func(a, b int) bool {
		if ds[a].d != ds[b].d {
			return ds[a].d < ds[b].d
		}
		return ds[a].c < ds[b].c
	})
	nearest := ds[0].d
	limit := float64(nearest) * s.boundaryEps
	added := 0
	for _, e := range ds {
		if added >= s.replicaCount {
			break
		}
		if added > 0 && float64(e.d) > limit {
			break
		}
		s.lists[e.c] = append(s.lists[e.c], spannEntry{pos: pos})
		added++
	}
}

// Search probes the nprobe nearest centroids via the centroid index, scans their
// posting lists, dedups replicated members, and returns the k nearest by exact
// distance (spec 08 §10.5, §10.6).
func (s *SPANN) Search(ctx context.Context, query []float32, k int, filter Bitmap, params SearchParams) ([]Candidate, error) {
	if len(query) != s.dim {
		return nil, ErrDimMismatch
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.searches.Add(1)
	if s.centroidIndex == nil || k <= 0 {
		return nil, nil
	}

	nprobe := params.NProbe
	if nprobe <= 0 {
		nprobe = s.nprobe
	}
	if nprobe > s.nlist {
		nprobe = s.nlist
	}

	// Find the nprobe nearest centroids. EfSearch is widened so the centroid index
	// returns at least nprobe results.
	cands, err := s.centroidIndex.Search(ctx, query, nprobe, nil, SearchParams{EfSearch: nprobe * 4})
	if err != nil {
		return nil, err
	}

	seen := make(map[uint32]struct{})
	out := make([]Candidate, 0, nprobe*16)
	for _, cc := range cands {
		for _, e := range s.lists[int(cc.Position)] {
			if _, dup := seen[e.pos]; dup {
				continue
			}
			seen[e.pos] = struct{}{}
			if _, dead := s.deleted[e.pos]; dead {
				continue
			}
			if filter != nil && !filter.Contains(e.pos) {
				continue
			}
			v, ok := s.vecs[e.pos]
			if !ok {
				continue
			}
			s.dcomp.Add(1)
			out = append(out, Candidate{Position: e.pos, Distance: s.dist(query, v)})
		}
	}
	sort.Slice(out, func(a, b int) bool { return minLess(out[a], out[b]) })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// Insert assigns a new point to its lists immediately (spec 08 §11.3). The spec's
// delta buffer with background consolidation (spec 08 §11.2) is the streaming slice
// over this correct foreground path.
func (s *SPANN) Insert(pos uint32, vec []float32) error {
	if len(vec) != s.dim {
		return ErrDimMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deleted, pos)
	vc := make([]float32, s.dim)
	copy(vc, vec)
	s.vecs[pos] = vc
	if s.centroidIndex == nil || s.nlist == 0 {
		// No centroids yet: a single implicit list holds everything until the next
		// rebuild trains real centroids.
		s.lists[0] = append(s.lists[0], spannEntry{pos: pos})
		return nil
	}
	s.removeFromLists(pos)
	s.assignReplicated(pos, vc)
	return nil
}

// Delete tombstones a position (spec 08 §11.5). Search skips tombstoned members;
// physical removal from posting lists happens at consolidation.
func (s *SPANN) Delete(pos uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vecs[pos]; !ok {
		return nil
	}
	s.deleted[pos] = struct{}{}
	return nil
}

// removeFromLists drops every reference to pos across posting lists, used when an
// Insert revises an existing point's assignment. The caller holds the write lock.
func (s *SPANN) removeFromLists(pos uint32) {
	for c, lst := range s.lists {
		kept := lst[:0]
		for _, e := range lst {
			if e.pos != pos {
				kept = append(kept, e)
			}
		}
		s.lists[c] = kept
	}
}

// Close releases index memory.
func (s *SPANN) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.centroidIndex != nil {
		_ = s.centroidIndex.Close()
		s.centroidIndex = nil
	}
	s.lists = nil
	s.vecs = nil
	s.deleted = nil
	s.centroids = nil
	return nil
}

// Stats returns a snapshot of index counters (spec 08 §19.1).
func (s *SPANN) Stats() IndexStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return IndexStats{
		NodeCount:        int64(len(s.vecs)),
		TombstoneCount:   int64(len(s.deleted)),
		DistComputations: s.dcomp.Load(),
		SearchCount:      s.searches.Load(),
		MemoryBytes:      s.memoryBytes(),
	}
}

// MemoryBytes estimates resident bytes (spec 08 §14.2).
func (s *SPANN) MemoryBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.memoryBytes()
}

func (s *SPANN) memoryBytes() int64 {
	var b int64
	b += int64(len(s.centroids)) * 4
	b += int64(len(s.vecs)) * int64(s.dim) * 4
	for _, lst := range s.lists {
		b += int64(len(lst)) * 4
	}
	if s.centroidIndex != nil {
		b += s.centroidIndex.MemoryBytes()
	}
	return b
}

// isqrt returns the integer square root of n (spec 08 §10.2 list-count heuristic).
func isqrt(n int) int {
	if n < 2 {
		return n
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}
