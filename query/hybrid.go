package query

import (
	"context"
	"fmt"

	"github.com/tamnd/vec/hybrid"
	"github.com/tamnd/vec/index"
	"github.com/tamnd/vec/storage"
)

// rankedSourceOp emits a precomputed ranked list (positions best-first) as a one-shot
// source, so a fused or keyword result can flow through the same gather and project
// stages as a dense result (spec 10 §16.1, spec 11 §10.3). The score rides in the
// batch's distance column unchanged; downstream stages preserve order.
type rankedSourceOp struct {
	list []hybrid.ScoredPos
	done bool
}

func (r *rankedSourceOp) Open(ec *ExecContext) error { return nil }

func (r *rankedSourceOp) Next(b *Batch) (int, error) {
	if r.done {
		return 0, nil
	}
	r.done = true
	n := len(r.list)
	if cap(b.Pos) < n {
		b.Pos = make([]uint32, n)
		b.Dist = make([]float32, n)
	}
	b.Pos = b.Pos[:n]
	b.Dist = b.Dist[:n]
	for i, sp := range r.list {
		b.Pos[i] = sp.Pos
		b.Dist[i] = float32(sp.Score)
	}
	b.NumRows = n
	b.Sel = nil
	b.NumSel = 0
	return n, nil
}

func (r *rankedSourceOp) Close() error { return nil }

// HybridRequest describes one fused query (spec 11 §10.3): the dense plan and vector,
// the extra ranked lists from the non-dense modalities (BM25, sparse, MaxSim) already
// scored by the hybrid package, the fusion method and weights, and the RRF constant.
// The dense list is always list 0 of the fusion; Weights, when set, line up with
// [dense, extra0, extra1, ...].
type HybridRequest struct {
	Plan    PhysicalPlan
	Vector  []float32
	Extra   [][]hybrid.ScoredPos
	Method  hybrid.FusionMethod
	Weights []float64
	RRFK    float64
	// OverFetch multiplies k for the dense candidate pool before fusion (spec 11
	// §10.3); 0 uses 3x.
	OverFetch int
}

// ExecuteHybrid runs a fused dense-plus-lexical query (spec 11 §10). It runs the dense
// pipeline to a ranked candidate pool, fuses it with the supplied modality lists,
// applies the metadata predicate to the fused result when the plan post-filters,
// trims to k, and assembles the rows. The non-dense lists are produced by the caller
// through the hybrid package, keeping this method modality-agnostic.
func (e *Executor) ExecuteHybrid(ctx context.Context, req HybridRequest) (rs ResultSet, err error) {
	if len(req.Vector) != e.coll.Dims {
		return ResultSet{}, ErrDimensionMismatch
	}
	plan := req.Plan
	over := req.OverFetch
	if over <= 0 {
		over = 3
	}

	snap := e.coll.Engine.Snapshot()
	stats := &QueryStats{}
	ec := &ExecContext{
		Ctx:      ctx,
		Snapshot: snap,
		Arena:    newQueryArena(e.memLimit),
		Pool:     e.pool,
		Stats:    stats,
		Coll:     e.coll,
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hybrid query panic: %v", r)
		}
	}()

	// Dense candidate pool: run the source and top-k of the plan with a widened k.
	densePlan := plan
	densePlan.K = plan.K * over
	densePlan.Rerank = false // fusion ranks; an exact rerank of the pool is wasted here
	dense, err := e.denseRanked(ec, densePlan, req.Vector)
	if err != nil {
		return ResultSet{}, err
	}

	// Fuse the dense pool with the modality lists.
	lists := make([][]hybrid.ScoredPos, 0, 1+len(req.Extra))
	lists = append(lists, dense)
	lists = append(lists, req.Extra...)
	fused := hybrid.Fuse(req.Method, lists, req.Weights, req.RRFK)
	stats.FuseCombined = int64(len(fused))

	// A post-filter predicate applies to the fused result (spec 11 §10.6).
	if plan.Filter == FilterPost && plan.Predicate != nil {
		bm, ferr := e.coll.Engine.MetadataFilter(e.coll.CollID, plan.Predicate, snap)
		if ferr != nil {
			return ResultSet{}, fmt.Errorf("%w: %v", ErrStorageRead, ferr)
		}
		kept := fused[:0]
		for _, sp := range fused {
			if bm.Contains(sp.Pos) {
				kept = append(kept, sp)
			}
		}
		fused = kept
	}
	fused = hybrid.TrimK(fused, plan.K)

	// Project the fused survivors: gather metadata, resolve point ids.
	var op physicalOp = &rankedSourceOp{list: fused}
	if len(plan.Project) > 0 {
		cols := make([]storage.ColID, 0, len(plan.Project))
		for _, name := range plan.Project {
			if id, ok := e.coll.colID(name); ok {
				cols = append(cols, id)
			}
		}
		op = &gatherOp{child: op, metaCols: cols}
	}
	op = &projectOp{child: op, colNames: plan.Project}

	rows, partial, reason, derr := drive(ec, op)
	if derr != nil {
		return ResultSet{}, derr
	}
	stats.ArenaUsedBytes = ec.Arena.Used()
	return ResultSet{Rows: rows, Partial: partial, PartialReason: reason, Stats: *stats}, nil
}

// denseRanked runs the dense source, optional post-filter, and top-k of a plan and
// returns the ranked candidate pool as scored positions, where the score is the
// negated distance so larger is better and the dense rank order is preserved for
// fusion (spec 11 §10.3).
func (e *Executor) denseRanked(ec *ExecContext, plan PhysicalPlan, vector []float32) ([]hybrid.ScoredPos, error) {
	root, err := e.buildRankingTree(ec, plan, vector, plan.EfSearch)
	if err != nil {
		return nil, err
	}
	if err := root.Open(ec); err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	var out []hybrid.ScoredPos
	var b Batch
	for {
		n, err := root.Next(&b)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		for i := 0; i < n; i++ {
			out = append(out, hybrid.ScoredPos{Pos: b.Pos[i], Score: -float64(b.Dist[i])})
		}
	}
	return out, nil
}

// buildRankingTree builds the dense pipeline up to top-k but stops before projection,
// so the caller gets ranked positions to fuse (spec 11 §10.3).
func (e *Executor) buildRankingTree(ec *ExecContext, plan PhysicalPlan, vector []float32, ef int) (physicalOp, error) {
	var bitmap *storage.PositionBitmap
	if (plan.Filter == FilterPre || plan.Filter == FilterIn) && plan.Predicate != nil {
		bm, err := e.coll.Engine.MetadataFilter(e.coll.CollID, plan.Predicate, ec.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStorageRead, err)
		}
		bitmap = bm
	}

	var src physicalOp
	switch plan.Path {
	case PathFlat:
		src = &flatScanOp{query: vector, maxK: plan.K, metric: plan.Metric, filter: bitmap}
	default:
		if e.coll.Index == nil {
			return nil, ErrNoIndex
		}
		var filter index.Bitmap
		if bitmap != nil {
			filter = bitmapFilter{bm: bitmap}
		}
		params := index.SearchParams{EfSearch: ef, NProbe: plan.NProbe, MaxCandidates: plan.K}
		src = &indexScanOp{query: vector, efSearch: ef, maxK: plan.K, filter: filter, params: params}
	}
	return &topKOp{child: src, k: plan.K}, nil
}
