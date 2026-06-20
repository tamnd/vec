package index

import (
	"context"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/tamnd/vector/distance"
	"github.com/tamnd/vector/quant"
)

// defaultNProbe is the IVF probe count when SearchParams.NProbe is unset
// (spec 08 §5.3): conservative, high-recall.
const defaultNProbe = 16

// defaultKMeansIter is the Lloyd iteration cap for coarse training (spec 08 §3.3).
const defaultKMeansIter = 25

// IVFConfig configures an inverted-file index (spec 08 §2, §4). A zero PQM builds
// plain full-precision IVF; a positive PQM builds IVFADC with PQ-encoded residuals
// (spec 08 §4). OPQ adds a learned rotation before residual PQ (spec 08 §7). PQ
// residual encoding is defined for the L2 family; for cosine/dot the index keeps
// full-precision entries and ignores PQM (spec 08 §17.3).
type IVFConfig struct {
	Dim      int
	Metric   Metric
	NList    int   // coarse cells; 0 derives sqrt(n) at build (spec 08 §5.2)
	NProbe   int   // default probe count; 0 uses defaultNProbe
	PQM      int   // PQ subspaces for residual codes; 0 = plain IVF
	PQNbits  int   // bits per subspace (default 8)
	UseOPQ   bool  // rotate residuals before PQ (spec 08 §7)
	Seed     int64 // reproducible centroid training
	KMeansIt int   // Lloyd cap; 0 uses defaultKMeansIter
}

// ivfEntry is one posting-list member: a position plus either its PQ residual code
// (IVFADC) or nil (plain IVF, the full vector lives in vecs).
type ivfEntry struct {
	pos  uint32
	code []byte
}

// IVF is the inverted-file index (spec 08 §2). It implements the Index SPI by
// partitioning the space into nlist Voronoi cells and scanning only the nprobe
// cells nearest a query. Like the HNSW index it holds full-precision vectors so it
// is self-contained ahead of the vector store; the spec's on-disk posting-list
// layout (spec 08 §2.7) is the storage-engine slice and Persist/Recover ship the
// whole index as one PageStore blob.
type IVF struct {
	mu sync.RWMutex

	dim      int
	metric   Metric
	dist     func(a, b []float32) float32
	nlist    int
	nprobe   int
	pqm      int
	pqnbits  int
	useOPQ   bool
	seed     int64
	kmeansIt int

	centroids []float32      // nlist*dim, row-major
	lists     [][]ivfEntry   // per-cell posting lists
	assignOf  map[uint32]int // pos -> list id (for delete bookkeeping)
	vecs      map[uint32][]float32
	deleted   map[uint32]struct{}
	codec     quant.Quantizer // residual codec (IVFADC), nil for plain IVF

	searches atomic.Int64
	dcomp    atomic.Int64
}

// NewIVF constructs an empty IVF index from a config (spec 08 §2).
func NewIVF(cfg IVFConfig) (*IVF, error) {
	if cfg.Dim <= 0 {
		return nil, ErrBadParams
	}
	nprobe := cfg.NProbe
	if nprobe <= 0 {
		nprobe = defaultNProbe
	}
	it := cfg.KMeansIt
	if it <= 0 {
		it = defaultKMeansIter
	}
	pqnbits := cfg.PQNbits
	if pqnbits <= 0 {
		pqnbits = 8
	}
	return &IVF{
		dim:      cfg.Dim,
		metric:   cfg.Metric,
		dist:     metricDistance(cfg.Metric),
		nlist:    cfg.NList,
		nprobe:   nprobe,
		pqm:      cfg.PQM,
		pqnbits:  pqnbits,
		useOPQ:   cfg.UseOPQ,
		seed:     cfg.Seed,
		kmeansIt: it,
		assignOf: make(map[uint32]int),
		vecs:     make(map[uint32][]float32),
		deleted:  make(map[uint32]struct{}),
	}, nil
}

// residualCodec reports whether IVFADC residual encoding applies: a positive PQM
// on an L2-family metric (spec 08 §4, §17.3).
func (idx *IVF) residualCodec() bool {
	return idx.pqm > 0 && (idx.metric == distance.L2Squared || idx.metric == distance.L2)
}

// Build trains the coarse quantizer and assigns every position (spec 08 §2.4).
func (idx *IVF) Build(ctx context.Context, positions []uint32, vectorAt func(uint32) []float32, params BuildParams) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if params.Seed != 0 {
		idx.seed = params.Seed
	}
	n := len(positions)
	if n == 0 {
		idx.centroids = nil
		idx.lists = nil
		return nil
	}

	// Resolve nlist: sqrt(n) rounded to a power of two by default; cold-start
	// reduction keeps at least ~39 points per cell (spec 08 §3.6, §5.2).
	nlist := idx.nlist
	if nlist <= 0 {
		nlist = roundPow2(int(math.Sqrt(float64(n))))
	}
	if nlist > n {
		nlist = n
	}
	if n < 39*nlist {
		nlist = n / 39
	}
	if nlist < 1 {
		nlist = 1
	}
	idx.nlist = nlist

	// Materialize the training matrix once.
	data := make([]float32, n*idx.dim)
	for i, p := range positions {
		v := vectorAt(p)
		copy(data[i*idx.dim:(i+1)*idx.dim], v)
	}

	idx.centroids = trainCentroids(data, n, idx.dim, nlist, idx.kmeansIt, idx.seed)

	// Train the residual codec (IVFADC) on residuals against assigned centroids.
	idx.codec = nil
	if idx.residualCodec() {
		res := make([]float32, n*idx.dim)
		for i := 0; i < n; i++ {
			c := idx.coarseQuantize(data[i*idx.dim : (i+1)*idx.dim])
			sub(res[i*idx.dim:(i+1)*idx.dim], data[i*idx.dim:(i+1)*idx.dim], idx.centroids[c*idx.dim:(c+1)*idx.dim])
		}
		codec, err := idx.trainResidualCodec(res, n)
		if err != nil {
			return err
		}
		idx.codec = codec
	}

	// Assign all points.
	idx.lists = make([][]ivfEntry, nlist)
	idx.assignOf = make(map[uint32]int, n)
	idx.vecs = make(map[uint32][]float32, n)
	idx.deleted = make(map[uint32]struct{})
	buf := make([]byte, 0)
	if idx.codec != nil {
		buf = make([]byte, idx.codec.CodeSize())
	}
	residual := make([]float32, idx.dim)
	for i, p := range positions {
		row := data[i*idx.dim : (i+1)*idx.dim]
		c := idx.coarseQuantize(row)
		entry := ivfEntry{pos: p}
		if idx.codec != nil {
			sub(residual, row, idx.centroids[c*idx.dim:(c+1)*idx.dim])
			code := make([]byte, len(buf))
			idx.codec.Encode(residual, code)
			entry.code = code
		}
		idx.lists[c] = append(idx.lists[c], entry)
		idx.assignOf[p] = c
		vc := make([]float32, idx.dim)
		copy(vc, row)
		idx.vecs[p] = vc
	}
	return nil
}

// trainResidualCodec fits a PQ or OPQ codebook to the residual sample (spec 08 §4,
// §7). It reuses the quant package's trainers and adapters.
func (idx *IVF) trainResidualCodec(res []float32, n int) (quant.Quantizer, error) {
	if idx.useOPQ {
		cb, err := quant.TrainOPQ(res, n, idx.dim, idx.pqm, idx.pqnbits, 20, idx.kmeansIt)
		if err != nil {
			return nil, err
		}
		return quant.NewOPQQuantizer(cb), nil
	}
	cb, err := quant.TrainPQ(res, n, idx.dim, idx.pqm, idx.pqnbits, idx.kmeansIt)
	if err != nil {
		return nil, err
	}
	return quant.NewPQQuantizer(cb), nil
}

// coarseQuantize returns the index of the nearest centroid by L2 (spec 08 §2.3).
func (idx *IVF) coarseQuantize(vec []float32) int {
	best, bestD := 0, float32(math.MaxFloat32)
	for c := 0; c < idx.nlist; c++ {
		d := distance.L2SquaredFloat32(vec, idx.centroids[c*idx.dim:(c+1)*idx.dim])
		if d < bestD {
			bestD, best = d, c
		}
	}
	return best
}

// Insert appends a point to its nearest cell without retraining (spec 08 §6.1).
func (idx *IVF) Insert(pos uint32, vec []float32) error {
	if len(vec) != idx.dim {
		return ErrDimMismatch
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.nlist == 0 || idx.centroids == nil {
		return ErrBadParams
	}
	delete(idx.deleted, pos)
	c := idx.coarseQuantize(vec)
	entry := ivfEntry{pos: pos}
	if idx.codec != nil {
		residual := make([]float32, idx.dim)
		sub(residual, vec, idx.centroids[c*idx.dim:(c+1)*idx.dim])
		code := make([]byte, idx.codec.CodeSize())
		idx.codec.Encode(residual, code)
		entry.code = code
	}
	idx.lists[c] = append(idx.lists[c], entry)
	idx.assignOf[pos] = c
	vc := make([]float32, idx.dim)
	copy(vc, vec)
	idx.vecs[pos] = vc
	return nil
}

// Delete tombstones a position (spec 08 §6.2). The posting-list entry is removed at
// rebuild; search skips tombstoned positions in the interim.
func (idx *IVF) Delete(pos uint32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if _, ok := idx.assignOf[pos]; !ok {
		return nil
	}
	idx.deleted[pos] = struct{}{}
	return nil
}

// Search probes the nprobe nearest cells and returns the top-k (spec 08 §2.5). For
// IVFADC it scans by ADC distance, then reranks the top candidates with the
// full-precision vectors (spec 08 §4.5).
func (idx *IVF) Search(ctx context.Context, query []float32, k int, filter Bitmap, params SearchParams) ([]Candidate, error) {
	if len(query) != idx.dim {
		return nil, ErrDimMismatch
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	idx.searches.Add(1)
	if idx.nlist == 0 || len(idx.centroids) == 0 || k <= 0 {
		return nil, nil
	}

	nprobe := params.NProbe
	if nprobe <= 0 {
		nprobe = idx.nprobe
	}
	if nprobe > idx.nlist {
		nprobe = idx.nlist
	}

	// Rank centroids by L2 to the query and take the nprobe nearest.
	type cd struct {
		id int
		d  float32
	}
	cds := make([]cd, idx.nlist)
	for c := 0; c < idx.nlist; c++ {
		cds[c] = cd{c, distance.L2SquaredFloat32(query, idx.centroids[c*idx.dim:(c+1)*idx.dim])}
	}
	sort.Slice(cds, func(a, b int) bool {
		if cds[a].d != cds[b].d {
			return cds[a].d < cds[b].d
		}
		return cds[a].id < cds[b].id
	})

	useADC := idx.codec != nil

	// Scan the probed lists into a candidate slice, ranked by ADC distance under
	// IVFADC and by the exact metric otherwise.
	out := make([]Candidate, 0)
	for p := 0; p < nprobe; p++ {
		c := cds[p].id
		var adc quant.ADCTable
		if useADC {
			residual := make([]float32, idx.dim)
			sub(residual, query, idx.centroids[c*idx.dim:(c+1)*idx.dim])
			adc = idx.codec.NewADCTable(residual, idx.metric)
		}
		for _, e := range idx.lists[c] {
			if _, dead := idx.deleted[e.pos]; dead {
				continue
			}
			if filter != nil && !filter.Contains(e.pos) {
				continue
			}
			var d float32
			if useADC {
				d = adc.Distance(e.code)
			} else {
				d = idx.dist(query, idx.vecs[e.pos])
			}
			idx.dcomp.Add(1)
			out = append(out, Candidate{Position: e.pos, Distance: d})
		}
	}
	sort.Slice(out, func(a, b int) bool { return minLess(out[a], out[b]) })

	if useADC {
		// Rerank the top candidates with full-precision vectors, then re-sort and
		// truncate to k (spec 08 §4.5).
		f := params.RerankFactor
		if f <= 0 {
			f = 3
		}
		rerankN := k * f
		if rerankN < 128 {
			rerankN = 128
		}
		if rerankN < len(out) {
			out = out[:rerankN]
		}
		for i := range out {
			out[i].Distance = idx.dist(query, idx.vecs[out[i].Position])
		}
		sort.Slice(out, func(a, b int) bool { return minLess(out[a], out[b]) })
	}
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// Close releases index memory.
func (idx *IVF) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.centroids = nil
	idx.lists = nil
	idx.vecs = nil
	idx.assignOf = nil
	idx.deleted = nil
	idx.codec = nil
	return nil
}

// Stats returns a snapshot of index counters (spec 08 §19.1).
func (idx *IVF) Stats() IndexStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	var nodes int64
	for _, l := range idx.lists {
		nodes += int64(len(l))
	}
	return IndexStats{
		NodeCount:        nodes,
		TombstoneCount:   int64(len(idx.deleted)),
		DistComputations: idx.dcomp.Load(),
		SearchCount:      idx.searches.Load(),
		MemoryBytes:      idx.memoryBytes(),
	}
}

// MemoryBytes estimates resident bytes (spec 08 §14.2).
func (idx *IVF) MemoryBytes() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.memoryBytes()
}

func (idx *IVF) memoryBytes() int64 {
	b := int64(len(idx.centroids)) * 4
	for _, l := range idx.lists {
		for _, e := range l {
			b += 4 + int64(len(e.code))
		}
	}
	b += int64(len(idx.vecs)) * int64(idx.dim) * 4
	return b
}

// sub writes a-b into dst (all length dim).
func sub(dst, a, b []float32) {
	for i := range dst {
		dst[i] = a[i] - b[i]
	}
}
