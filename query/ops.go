package query

import (
	"context"
	"fmt"
	"sort"

	"github.com/tamnd/vec/distance"
	"github.com/tamnd/vec/index"
	"github.com/tamnd/vec/storage"
)

// physicalOp is the vectorized operator interface (spec 10 §6.2). Open initializes
// state and opens children; Next produces the next batch (n>0 rows, 0 on EOF); Close
// releases resources. vec's read-path results are small, so most operators are
// pipeline breakers that produce all rows on the first Next and EOF after.
type physicalOp interface {
	Open(ec *ExecContext) error
	Next(b *Batch) (int, error)
	Close() error
}

// rankKernel returns the distance function for a metric where smaller means closer
// (spec 10 §8.4), matching the index SPI's ranking orientation (index §4.2).
func rankKernel(m distance.Metric) func(a, b []float32) float32 {
	switch m {
	case distance.Cosine:
		return distance.CosineDistanceFloat32
	case distance.Dot:
		return func(a, b []float32) float32 { return -distance.DotFloat32(a, b) }
	default: // L2, L2Squared
		return distance.L2SquaredFloat32
	}
}

// ----- IndexScan: the ANN source (spec 10 §2.5, §16.1) -----

type indexScanOp struct {
	ec       *ExecContext
	query    []float32
	efSearch int
	maxK     int
	filter   index.Bitmap // nil for unfiltered / post-filter
	params   index.SearchParams
	cands    []index.Candidate
	done     bool
}

func (s *indexScanOp) Open(ec *ExecContext) error {
	s.ec = ec
	cands, err := ec.Coll.Index.Search(ec.Ctx, s.query, s.maxK, s.filter, s.params)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIndexSearch, err)
	}
	s.cands = cands
	ec.Stats.ANNCandidatesReturned += int64(len(cands))
	return nil
}

func (s *indexScanOp) Next(b *Batch) (int, error) {
	if s.done {
		return 0, nil
	}
	s.done = true
	return writeCandidates(b, s.cands), nil
}

func (s *indexScanOp) Close() error { s.cands = nil; return nil }

// ----- FlatScan: the brute-force source, morsel-parallel (spec 10 §12.2) -----

type flatScanOp struct {
	ec     *ExecContext
	query  []float32
	maxK   int
	metric distance.Metric
	filter *storage.PositionBitmap // nil = scan all live positions
	cands  []index.Candidate
	done   bool
}

func (f *flatScanOp) Open(ec *ExecContext) error {
	f.ec = ec
	kernel := rankKernel(f.metric)

	// Collect (pos, vec) pairs from a snapshot scan, honoring the optional filter.
	type pv struct {
		pos uint32
		vec []float32
	}
	var items []pv
	err := ec.Coll.Engine.ScanVectors(ec.Coll.CollID, ec.Snapshot, func(pos uint32, vec []float32) bool {
		if f.filter == nil || f.filter.Contains(pos) {
			cp := make([]float32, len(vec))
			copy(cp, vec)
			items = append(items, pv{pos, cp})
		}
		return true
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStorageRead, err)
	}
	ec.Stats.FlatScanned += int64(len(items))

	// Morsel-parallel distance computation into per-worker heaps, then merge.
	workers := ec.Pool.Workers()
	if workers > len(items) {
		workers = len(items)
	}
	if workers < 1 {
		workers = 1
	}
	heaps := make([]*topKHeap, workers)
	for i := range heaps {
		heaps[i] = newTopKHeap(f.maxK)
	}
	chunk := (len(items) + workers - 1) / max1(workers)
	ec.Pool.run(workers, func(w int) {
		lo := w * chunk
		hi := lo + chunk
		if hi > len(items) {
			hi = len(items)
		}
		h := heaps[w]
		for i := lo; i < hi; i++ {
			d := kernel(f.query, items[i].vec)
			h.push(index.Candidate{Position: items[i].pos, Distance: d})
		}
	})
	merged := newTopKHeap(f.maxK)
	for _, h := range heaps {
		for _, c := range h.data {
			merged.push(c)
		}
	}
	f.cands = merged.drain()
	return nil
}

func (f *flatScanOp) Next(b *Batch) (int, error) {
	if f.done {
		return 0, nil
	}
	f.done = true
	return writeCandidates(b, f.cands), nil
}

func (f *flatScanOp) Close() error { f.cands = nil; return nil }

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// ----- TopK: bounded heap select (spec 10 §16.2) -----

type topKOp struct {
	child physicalOp
	k     int
	done  bool
}

func (t *topKOp) Open(ec *ExecContext) error { return t.child.Open(ec) }

func (t *topKOp) Next(b *Batch) (int, error) {
	if t.done {
		return 0, nil
	}
	t.done = true
	h := newTopKHeap(t.k)
	var tmp Batch
	for {
		n, err := t.child.Next(&tmp)
		if err != nil {
			return 0, err
		}
		if n == 0 {
			break
		}
		if tmp.Sel == nil {
			for i := 0; i < n; i++ {
				h.push(index.Candidate{Position: tmp.Pos[i], Distance: tmp.Dist[i]})
			}
		} else {
			for _, idx := range tmp.Sel[:tmp.NumSel] {
				h.push(index.Candidate{Position: tmp.Pos[idx], Distance: tmp.Dist[idx]})
			}
		}
	}
	return writeCandidates(b, h.drain()), nil
}

func (t *topKOp) Close() error { return t.child.Close() }

// ----- PostFilter: drop candidates failing the predicate (spec 10 §9.5) -----

type postFilterOp struct {
	child  physicalOp
	bitmap *storage.PositionBitmap
}

func (pf *postFilterOp) Open(ec *ExecContext) error { return pf.child.Open(ec) }

func (pf *postFilterOp) Next(b *Batch) (int, error) {
	n, err := pf.child.Next(b)
	if n == 0 || err != nil {
		return n, err
	}
	sel := b.Sel[:0]
	if cap(b.Sel) < n {
		sel = make([]uint16, 0, n)
	}
	survived := 0
	base := b.Sel
	count := n
	idxs := func(i int) int { return i }
	if base != nil {
		count = b.NumSel
		idxs = func(i int) int { return int(base[i]) }
	}
	for i := 0; i < count; i++ {
		row := idxs(i)
		if pf.bitmap.Contains(b.Pos[row]) {
			sel = append(sel, uint16(row))
			survived++
		}
	}
	b.Sel = sel
	b.NumSel = len(sel)
	return n, nil
}

func (pf *postFilterOp) Close() error { return pf.child.Close() }

// ----- Gather: late-materialize vectors and/or metadata (spec 10 §16.3) -----

type gatherOp struct {
	ec        *ExecContext
	child     physicalOp
	gatherVec bool
	metaCols  []storage.ColID
}

func (g *gatherOp) Open(ec *ExecContext) error {
	g.ec = ec
	return g.child.Open(ec)
}

func (g *gatherOp) Next(b *Batch) (int, error) {
	n, err := g.child.Next(b)
	if n == 0 || err != nil {
		return n, err
	}
	coll := g.ec.Coll
	if g.gatherVec {
		b.Vecs = make([][]float32, n)
		for i := 0; i < n; i++ {
			buf := make([]float32, coll.Dims)
			if err := coll.Engine.FetchVector(coll.CollID, b.Pos[i], buf); err != nil {
				return 0, fmt.Errorf("%w: %v", ErrStorageRead, err)
			}
			b.Vecs[i] = buf
		}
	}
	if len(g.metaCols) > 0 {
		b.Meta = make([]storage.MetaRow, n)
		for i := 0; i < n; i++ {
			rec, err := coll.Engine.Fetch(coll.CollID, b.Pos[i], g.metaCols, g.ec.Snapshot)
			if err != nil {
				return 0, fmt.Errorf("%w: %v", ErrStorageRead, err)
			}
			b.Meta[i] = rec.Meta
		}
	}
	return n, nil
}

func (g *gatherOp) Close() error { return g.child.Close() }

// ----- Rerank: exact re-scoring of gathered candidates (spec 10 §16.4) -----

type rerankOp struct {
	ec     *ExecContext
	child  physicalOp
	query  []float32
	metric distance.Metric
	k      int
	done   bool
}

func (rr *rerankOp) Open(ec *ExecContext) error {
	rr.ec = ec
	return rr.child.Open(ec)
}

func (rr *rerankOp) Next(b *Batch) (int, error) {
	if rr.done {
		return 0, nil
	}
	rr.done = true
	var tmp Batch
	n, err := rr.child.Next(&tmp)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	kernel := rankKernel(rr.metric)
	cands := make([]index.Candidate, 0, n)
	for i := 0; i < n; i++ {
		d := kernel(rr.query, tmp.Vecs[i])
		cands = append(cands, index.Candidate{Position: tmp.Pos[i], Distance: d})
	}
	rr.ec.Stats.RerankCandidates += int32(len(cands))
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Distance != cands[j].Distance {
			return greaterDist(cands[j].Distance, cands[i].Distance) // ascending
		}
		return cands[i].Position < cands[j].Position
	})
	if len(cands) > rr.k {
		cands = cands[:rr.k]
	}
	return writeCandidates(b, cands), nil
}

func (rr *rerankOp) Close() error { return rr.child.Close() }

// ----- Project: resolve point ids and assemble output (spec 10 §16.6) -----

type projectOp struct {
	ec       *ExecContext
	child    physicalOp
	colNames []string
	colIDs   []storage.ColID
	done     bool
}

func (p *projectOp) Open(ec *ExecContext) error {
	p.ec = ec
	return p.child.Open(ec)
}

func (p *projectOp) Next(b *Batch) (int, error) {
	if p.done {
		return 0, nil
	}
	p.done = true
	n, err := p.child.Next(b)
	if n == 0 || err != nil {
		return n, err
	}
	b.PointID = make([]uint64, n)
	for i := 0; i < n; i++ {
		pid, err := p.ec.Coll.Engine.LookupPos(p.ec.Coll.CollID, b.Pos[i])
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrStorageRead, err)
		}
		b.PointID[i] = uint64(pid)
	}
	return n, nil
}

func (p *projectOp) Close() error { return p.child.Close() }

// writeCandidates copies a sorted candidate slice into a batch's position and
// distance columns, sizing the columns to fit.
func writeCandidates(b *Batch, cands []index.Candidate) int {
	n := len(cands)
	if cap(b.Pos) < n {
		b.Pos = make([]uint32, n)
		b.Dist = make([]float32, n)
	}
	b.Pos = b.Pos[:n]
	b.Dist = b.Dist[:n]
	for i, c := range cands {
		b.Pos[i] = c.Position
		b.Dist[i] = c.Distance
	}
	b.NumRows = n
	b.Sel = nil
	b.NumSel = 0
	return n
}

var _ = context.Background
