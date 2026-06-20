package index

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// Flat is the brute-force baseline index (spec 07 §1, §11.4). It scans every live
// position and computes the exact distance, so its results are the recall oracle
// the HNSW tests measure against. The planner selects it for small collections and
// for the very-selective-filter fallback where an exact scan beats graph traversal
// (spec 07 §11.4).
type Flat struct {
	mu       sync.RWMutex
	dim      int
	metric   Metric
	dist     func(a, b []float32) float32
	vecs     map[uint32][]float32
	deleted  map[uint32]struct{}
	closed   bool
	searches atomic.Int64
	dcomp    atomic.Int64
}

// NewFlat returns an empty flat index for the given dimension and metric.
func NewFlat(dim int, metric Metric) *Flat {
	return &Flat{
		dim:     dim,
		metric:  metric,
		dist:    metricDistance(metric),
		vecs:    make(map[uint32][]float32),
		deleted: make(map[uint32]struct{}),
	}
}

func (f *Flat) Build(ctx context.Context, positions []uint32, vectorAt func(uint32) []float32, params BuildParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if params.Metric != f.metric && params.Metric != 0 {
		f.metric = params.Metric
		f.dist = metricDistance(params.Metric)
	}
	f.vecs = make(map[uint32][]float32, len(positions))
	f.deleted = make(map[uint32]struct{})
	for _, p := range positions {
		if err := ctx.Err(); err != nil {
			return err
		}
		v := vectorAt(p)
		cp := make([]float32, len(v))
		copy(cp, v)
		f.vecs[p] = cp
	}
	return nil
}

func (f *Flat) Insert(pos uint32, vec []float32) error {
	if len(vec) != f.dim {
		return ErrDimMismatch
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	cp := make([]float32, len(vec))
	copy(cp, vec)
	f.vecs[pos] = cp
	delete(f.deleted, pos)
	return nil
}

func (f *Flat) Delete(pos uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	if _, ok := f.vecs[pos]; ok {
		f.deleted[pos] = struct{}{}
	}
	return nil
}

func (f *Flat) Search(ctx context.Context, query []float32, k int, filter Bitmap, params SearchParams) ([]Candidate, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, ErrClosed
	}
	f.searches.Add(1)
	out := make([]Candidate, 0, len(f.vecs))
	for pos, v := range f.vecs {
		if _, dead := f.deleted[pos]; dead {
			continue
		}
		if filter != nil && !filter.Contains(pos) {
			continue
		}
		out = append(out, Candidate{Position: pos, Distance: f.dist(query, v)})
	}
	f.dcomp.Add(int64(len(out)))
	sort.Slice(out, func(i, j int) bool { return minLess(out[i], out[j]) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func (f *Flat) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.vecs = nil
	f.deleted = nil
	return nil
}

func (f *Flat) Stats() IndexStats {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return IndexStats{
		NodeCount:        int64(len(f.vecs) - len(f.deleted)),
		TombstoneCount:   int64(len(f.deleted)),
		DistComputations: f.dcomp.Load(),
		SearchCount:      f.searches.Load(),
		MemoryBytes:      f.MemoryBytes(),
	}
}

func (f *Flat) MemoryBytes() int64 {
	return int64(len(f.vecs)) * int64(f.dim*4+8)
}

// Persist serializes the flat index to one blob: dim, metric, then each live
// position and its vector (spec 07 §9.1, simplified to a blob seam).
func (f *Flat) Persist(ps PageStore) error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	buf := make([]byte, 0, 16+len(f.vecs)*(4+f.dim*4))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(f.dim))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(f.metric))
	live := 0
	for pos := range f.vecs {
		if _, dead := f.deleted[pos]; !dead {
			live++
		}
	}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(live))
	for pos, v := range f.vecs {
		if _, dead := f.deleted[pos]; dead {
			continue
		}
		buf = binary.LittleEndian.AppendUint32(buf, pos)
		for _, x := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(x))
		}
	}
	return ps.PutBlob(buf)
}

// Recover reads a blob written by Persist.
func (f *Flat) Recover(ps PageStore) error {
	b, err := ps.GetBlob()
	if err != nil {
		return err
	}
	if len(b) < 12 {
		return ErrIndexCorrupt
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dim = int(binary.LittleEndian.Uint32(b[0:]))
	f.metric = Metric(binary.LittleEndian.Uint32(b[4:]))
	f.dist = metricDistance(f.metric)
	n := int(binary.LittleEndian.Uint32(b[8:]))
	f.vecs = make(map[uint32][]float32, n)
	f.deleted = make(map[uint32]struct{})
	off := 12
	rec := 4 + f.dim*4
	for i := 0; i < n; i++ {
		if off+rec > len(b) {
			return ErrIndexCorrupt
		}
		pos := binary.LittleEndian.Uint32(b[off:])
		off += 4
		v := make([]float32, f.dim)
		for j := 0; j < f.dim; j++ {
			v[j] = math.Float32frombits(binary.LittleEndian.Uint32(b[off:]))
			off += 4
		}
		f.vecs[pos] = v
	}
	return nil
}
