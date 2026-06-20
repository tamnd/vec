package query

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/tamnd/vec/index"
	"github.com/tamnd/vec/storage"
)

// Executor runs a PhysicalPlan against a collection (spec 10 §1.2). One executor is
// bound to one collection; it is safe for concurrent Execute calls because every
// query gets its own snapshot, arena, stats, and operator tree.
type Executor struct {
	coll       *Collection
	pool       *WorkerPool
	memLimit   int64
	defTimeout time.Duration
}

// ExecutorOption configures an executor.
type ExecutorOption func(*Executor)

// WithWorkers sets the parallelism degree for flat scans (spec 10 §12.5).
func WithWorkers(n int) ExecutorOption {
	return func(e *Executor) { e.pool = NewWorkerPool(n) }
}

// WithMemoryLimit caps per-query arena bytes; a query that exceeds it fails with
// ErrQueryMemoryExceeded (spec 10 §14.3).
func WithMemoryLimit(bytes int64) ExecutorOption {
	return func(e *Executor) { e.memLimit = bytes }
}

// WithDefaultTimeout sets the deadline applied to queries that do not carry their own
// (spec 10 §15.2).
func WithDefaultTimeout(d time.Duration) ExecutorOption {
	return func(e *Executor) { e.defTimeout = d }
}

// NewExecutor binds an executor to a collection (spec 10 §18.1).
func NewExecutor(coll *Collection, opts ...ExecutorOption) *Executor {
	e := &Executor{
		coll: coll,
		pool: NewWorkerPool(runtime.GOMAXPROCS(0)),
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Execute runs a query end to end: pin a snapshot, derive the deadline, build the
// operator tree from the plan, drive it to completion, and assemble the result set
// (spec 10 §1.2). A cancelled or timed-out query returns the rows gathered so far
// with Partial set (spec 10 §15.4). A panic in an operator (including an arena
// overflow) is recovered into an error (spec 10 §17.2).
func (e *Executor) Execute(ctx context.Context, plan PhysicalPlan, vector []float32) (rs ResultSet, err error) {
	if len(vector) != e.coll.Dims {
		return ResultSet{}, ErrDimensionMismatch
	}

	timeout := plan.Timeout
	if timeout <= 0 {
		timeout = e.defTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	snap := e.coll.Engine.Snapshot()
	arena := newQueryArena(e.memLimit)
	stats := &QueryStats{}
	ec := &ExecContext{
		Ctx:      ctx,
		Snapshot: snap,
		Arena:    arena,
		Pool:     e.pool,
		Stats:    stats,
		Coll:     e.coll,
	}

	defer func() {
		if r := recover(); r != nil {
			if me, ok := r.(error); ok && errors.Is(me, ErrQueryMemoryExceeded) {
				err = me
				return
			}
			err = fmt.Errorf("query panic: %v", r)
		}
	}()

	rs, err = e.runPlan(ec, plan, vector)
	stats.ArenaUsedBytes = arena.Used()
	rs.Stats = *stats
	return rs, err
}

// runPlan builds the operator tree for plan and drives it, applying adaptive ef
// widening when a post-filtered HNSW search returns fewer than k survivors.
func (e *Executor) runPlan(ec *ExecContext, plan PhysicalPlan, vector []float32) (ResultSet, error) {
	ef := plan.EfSearch
	maxEf := plan.MaxEfSearch
	for {
		root, err := e.buildTree(ec, plan, vector, ef)
		if err != nil {
			return ResultSet{}, err
		}
		rows, partial, reason, err := drive(ec, root)
		if err != nil {
			return ResultSet{}, err
		}

		// Adaptive widening: a post-filtered HNSW walk can exhaust its survivors
		// before k is filled. Re-run with a doubled beam, bounded by MaxEfSearch
		// (spec 10 §1.5, §4.6).
		if plan.AllowWiden && !partial && len(rows) < plan.K && ef < maxEf {
			ec.Stats.ANNRetries++
			ef *= 2
			if ef > maxEf {
				ef = maxEf
			}
			continue
		}
		return ResultSet{Rows: rows, Partial: partial, PartialReason: reason}, nil
	}
}

// buildTree assembles the physical operator pipeline from the plan (spec 10 §16).
// The pipeline is, bottom to top: a source (index or flat) -> optional post-filter
// -> optional gather+rerank -> top-k -> gather metadata -> project.
func (e *Executor) buildTree(ec *ExecContext, plan PhysicalPlan, vector []float32, ef int) (physicalOp, error) {
	// Resolve the filter bitmap once; both pre and post strategies share it.
	var bitmap *storage.PositionBitmap
	if plan.Filter != FilterNone && plan.Predicate != nil {
		bm, err := e.coll.Engine.MetadataFilter(e.coll.CollID, plan.Predicate, ec.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStorageRead, err)
		}
		bitmap = bm
		ec.Stats.PreFilterBitmapSize = int64(bm.Count())
	}

	// How many candidates the source should surface before top-k. Rerank widens the
	// candidate pool; a plain search asks for k directly.
	maxK := plan.K
	if plan.Rerank {
		r := plan.RerankR
		if r < plan.K {
			r = plan.K * 4
		}
		maxK = r
	}

	var src physicalOp
	switch plan.Path {
	case PathFlat:
		var f *storage.PositionBitmap
		if plan.Filter != FilterNone {
			f = bitmap
		}
		src = &flatScanOp{query: vector, maxK: maxK, metric: plan.Metric, filter: f}
	default: // index path
		if e.coll.Index == nil {
			return nil, ErrNoIndex
		}
		var filter index.Bitmap
		if plan.Filter == FilterPre || plan.Filter == FilterIn {
			filter = bitmapFilter{bm: bitmap}
		}
		params := index.SearchParams{
			EfSearch:      ef,
			NProbe:        plan.NProbe,
			MaxCandidates: maxK,
		}
		src = &indexScanOp{query: vector, efSearch: ef, maxK: maxK, filter: filter, params: params}
	}

	op := src

	// Post-filter drops candidates outside the bitmap after an unfiltered walk.
	if plan.Filter == FilterPost {
		op = &postFilterOp{child: op, bitmap: bitmap}
	}

	// Rerank: gather full vectors for the widened candidate set, recompute exact
	// distances, and keep the true top-k.
	if plan.Rerank {
		op = &gatherOp{child: op, gatherVec: true}
		op = &rerankOp{child: op, query: vector, metric: plan.Metric, k: plan.K}
	} else {
		// Bound to k with the heap (the source may have returned maxK == k already,
		// but post-filter can reorder selection, so top-k normalizes ordering).
		op = &topKOp{child: op, k: plan.K}
	}

	// Gather projected metadata for the survivors, then resolve point ids.
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

	return op, nil
}

// drive opens the operator tree, pulls every batch, and assembles result rows
// (spec 10 §16.7). It honors context cancellation between batches and reports a
// partial result rather than discarding work already done.
func drive(ec *ExecContext, root physicalOp) ([]Row, bool, string, error) {
	if err := root.Open(ec); err != nil {
		return nil, false, "", err
	}
	defer root.Close()

	var rows []Row
	partial := false
	reason := ""
	colNames := projectionNames(root)

	var b Batch
	for {
		if err := ec.Ctx.Err(); err != nil {
			partial = true
			reason = err.Error()
			break
		}
		n, err := root.Next(&b)
		if err != nil {
			// A cancellation surfaced through an operator is a partial result, not a
			// hard failure (spec 10 §15.4).
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				partial = true
				reason = err.Error()
				break
			}
			return nil, false, "", err
		}
		if n == 0 {
			break
		}
		rows = appendRows(rows, &b, colNames, ec.Coll)
	}
	return rows, partial, reason, nil
}

// projectionNames extracts the projected column names from the root project op.
func projectionNames(root physicalOp) []string {
	if p, ok := root.(*projectOp); ok {
		return p.colNames
	}
	return nil
}

// appendRows turns a batch's live rows into result rows, decoding projected
// metadata cells into model values keyed by column name.
func appendRows(dst []Row, b *Batch, colNames []string, coll *Collection) []Row {
	n := b.NumRows
	for i := 0; i < n; i++ {
		row := Row{
			PointID:  storage.PointID(b.PointID[i]),
			Distance: b.Dist[i],
		}
		if b.Meta != nil && len(colNames) > 0 {
			row.Meta = make(map[string]storage.Value, len(colNames))
			meta := b.Meta[i]
			for _, name := range colNames {
				if id, ok := coll.colID(name); ok {
					if v, present := meta[id]; present {
						row.Meta[name] = v
					}
				}
			}
		}
		dst = append(dst, row)
	}
	return dst
}
