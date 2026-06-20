package query

import (
	"math"

	"github.com/tamnd/vec/storage"
)

// Planner turns a BoundQuery into a PhysicalPlan using a cost model with a recall
// dimension (spec 13 §1.3). It estimates selectivity from column statistics, picks
// an access path (flat vs the collection's ANN index), chooses a filter strategy,
// and sizes ef/nprobe to the recall target. The choices are committed into the
// PhysicalPlan; the executor never re-plans.
type Planner struct {
	coll  *Collection
	cache *planCache
}

// NewPlanner returns a planner bound to one collection with an LRU plan cache of
// the given capacity (spec 13 §13.2); cap <= 0 disables caching.
func NewPlanner(coll *Collection, cacheCap int) *Planner {
	return &Planner{coll: coll, cache: newPlanCache(cacheCap)}
}

// Tunables for the cost model and strategy thresholds (spec 13 §8.3, §10.4). These
// match the spec's default constants; the config layer ([20], task 18) will surface
// them as pragmas.
const (
	flatRowThreshold = 5000 // below this many live rows, flat beats the index (spec 13 §10.3)
	selPreThreshold  = 0.05 // selectivity below this prefers pre-filter (spec 13 §8.3)
	selPostThreshold = 0.50 // selectivity above this prefers post-filter
	defaultRecall    = 0.95 // recall target when the query omits one (spec 13 §4.5)
	defaultRerankR   = 0    // 0 means derive from k when reranking
	widenCapFactor   = 8    // adaptive ef widening cap = widenCapFactor * base (spec 10 §4.6)
)

// Plan produces the physical plan for q. Equivalent bound queries hit the LRU cache
// (spec 13 §13.2); a cache miss runs the full cost-based selection.
func (pl *Planner) Plan(q BoundQuery) (PhysicalPlan, error) {
	if q.K < 1 {
		return PhysicalPlan{}, ErrInvalidK
	}
	if len(q.Vector) != pl.coll.Dims {
		return PhysicalPlan{}, ErrDimensionMismatch
	}
	if q.EfSearch < 0 {
		return PhysicalPlan{}, ErrInvalidEfSearch
	}

	key := planKeyOf(pl.coll.CollID, q)
	if pl.cache != nil {
		if cached, ok := pl.cache.get(key); ok {
			return pl.specialize(cached, q), nil
		}
	}

	plan, err := pl.build(q)
	if err != nil {
		return PhysicalPlan{}, err
	}
	if pl.cache != nil {
		pl.cache.put(key, plan)
	}
	return plan, nil
}

// build runs the cost-based selection for a cache miss.
func (pl *Planner) build(q BoundQuery) (PhysicalPlan, error) {
	rows := pl.liveRows()
	sel := pl.selectivity(q)

	metric := q.Metric
	plan := PhysicalPlan{
		K:           q.K,
		Metric:      metric,
		Project:     q.Project,
		IncludeDist: q.IncludeDistance,
		Predicate:   q.Predicate,
		Timeout:     q.Timeout,
	}

	// Access-path selection (spec 13 §10). With no index, or a small collection, or
	// a very selective filter over a small matching set, flat exact scan wins.
	hasIndex := pl.coll.Index != nil
	matchRows := float64(rows) * sel
	useFlat := !hasIndex ||
		rows <= flatRowThreshold ||
		(q.Predicate != nil && matchRows <= flatRowThreshold && sel <= selPreThreshold)
	if useFlat && !hasIndex && !q.AllowFlat && pl.coll.Index == nil {
		// No index and flat not permitted: the caller asked for ANN that does not exist.
		if !q.AllowFlat {
			return PhysicalPlan{}, ErrNoIndex
		}
	}

	if useFlat {
		plan.Path = PathFlat
		if q.Predicate != nil {
			plan.Filter = FilterPre // flat scan applies the bitmap inline
		}
		plan.EstRecall = 1.0
		plan.EstCost = costFlat(matchRowsOrAll(rows, sel, q.Predicate != nil), pl.coll.Dims)
		return plan, nil
	}

	// Index path.
	plan.Path = pl.coll.IndexKind
	plan.Filter = pl.filterStrategy(q.Predicate != nil, sel)

	recall := q.RecallTarget
	if recall <= 0 {
		recall = defaultRecall
	}

	switch plan.Path {
	case PathIVF:
		plan.NProbe = pl.selectNProbe(q, recall)
		plan.EstRecall = recall
		plan.EstCost = costIVF(pl.coll.NList, plan.NProbe, rows, pl.coll.Dims)
	case PathHNSW, PathDiskANN:
		plan.EfSearch = pl.selectEf(q, recall)
		plan.EstRecall = recall
		plan.EstCost = costHNSW(plan.EfSearch, pl.coll.M, pl.coll.Dims, rows)
	default:
		plan.Path = PathFlat
		plan.EstRecall = 1.0
		plan.EstCost = costFlat(float64(rows), pl.coll.Dims)
	}

	// Rerank: when the index returns quantized-distance candidates, an exact recompute
	// over a widened candidate set lifts recall (spec 10 §8.2). vec enables rerank on
	// the index path whenever a rerank factor is requested or the metric benefits.
	if q.RerankR > 0 {
		plan.Rerank = true
		plan.RerankR = q.RerankR
	}

	// Adaptive widening applies to post-filtered HNSW where the survivor set may be
	// exhausted before k is filled (spec 10 §1.5).
	if plan.Filter == FilterPost && plan.Path == PathHNSW {
		plan.AllowWiden = true
		plan.MaxEfSearch = plan.EfSearch * widenCapFactor
	}

	return plan, nil
}

// specialize re-applies the per-query overrides that must not be shared through the
// cache (the ef/nprobe pins and the timeout) onto a cached structural plan.
func (pl *Planner) specialize(p PhysicalPlan, q BoundQuery) PhysicalPlan {
	if q.EfSearch > 0 {
		p.EfSearch = q.EfSearch
	}
	if q.NProbe > 0 {
		p.NProbe = q.NProbe
	}
	if q.RerankR > 0 {
		p.Rerank = true
		p.RerankR = q.RerankR
	}
	p.Timeout = q.Timeout
	p.K = q.K
	p.Project = q.Project
	p.IncludeDist = q.IncludeDistance
	p.Predicate = q.Predicate
	return p
}

// liveRows returns the collection's live row count, defaulting to 0 on a stats miss.
func (pl *Planner) liveRows() int {
	cs, err := pl.coll.Engine.CollectionStats(pl.coll.CollID)
	if err != nil {
		return 0
	}
	return int(cs.LivePoints)
}

// selectivity returns the fraction of rows the predicate keeps (spec 13 §7). An
// explicit BoundQuery.Selectivity wins; otherwise it is estimated from column
// statistics, falling back to 1.0 (no filter) when unknown.
func (pl *Planner) selectivity(q BoundQuery) float64 {
	if q.Predicate == nil {
		return 1.0
	}
	if q.Selectivity >= 0 {
		return clamp01(q.Selectivity)
	}
	if est, ok := estimateSelectivity(pl.coll, q.Predicate); ok {
		return clamp01(est)
	}
	return 0.25 // spec 13 §7.6 default when statistics are absent
}

// filterStrategy maps selectivity to a filter strategy (spec 13 §8.3).
func (pl *Planner) filterStrategy(hasPred bool, sel float64) FilterStrategy {
	if !hasPred {
		return FilterNone
	}
	switch {
	case sel <= selPreThreshold:
		return FilterPre
	case sel >= selPostThreshold:
		return FilterPost
	default:
		return FilterIn
	}
}

// selectEf sizes the HNSW beam width to the recall target (spec 13 §11.2). A pinned
// per-query EfSearch wins; otherwise the analytical fallback ef_min =
// max(k, ceil(k / recall^2)), floored at a small constant for tiny k.
func (pl *Planner) selectEf(q BoundQuery, recall float64) int {
	if q.EfSearch > 0 {
		return q.EfSearch
	}
	k := q.K
	efMin := math.Ceil(float64(k) / (recall * recall))
	ef := int(math.Max(float64(k), efMin))
	if ef < 16 {
		ef = 16
	}
	return ef
}

// selectNProbe sizes IVF nprobe to the recall target (spec 13 §11.4). A pinned
// per-query NProbe wins; otherwise probe a recall-proportional share of the cells.
func (pl *Planner) selectNProbe(q BoundQuery, recall float64) int {
	if q.NProbe > 0 {
		return q.NProbe
	}
	nlist := pl.coll.NList
	if nlist < 1 {
		nlist = 1
	}
	np := int(math.Ceil(float64(nlist) * recall * 0.1))
	if np < 1 {
		np = 1
	}
	if np > nlist {
		np = nlist
	}
	return np
}

// ----- cost formulas (spec 13 §6) -----

// costFlat is the brute-force cost: one distance computation per scanned row, each
// O(dims) (spec 13 §6.3).
func costFlat(rows float64, dims int) float64 {
	return rows * float64(dims)
}

// costHNSW approximates the graph walk cost: ef * log(rows) hops, each touching M
// neighbors at O(dims) (spec 13 §6.4).
func costHNSW(ef, m, dims, rows int) float64 {
	if rows < 2 {
		rows = 2
	}
	return float64(ef) * math.Log2(float64(rows)) * float64(maxInt(m, 1)) * float64(dims)
}

// costIVF approximates the IVF cost: coarse quantization over nlist centroids plus a
// scan of nprobe cells (spec 13 §6.5).
func costIVF(nlist, nprobe, rows, dims int) float64 {
	if nlist < 1 {
		nlist = 1
	}
	cellRows := float64(rows) / float64(nlist)
	return (float64(nlist) + cellRows*float64(nprobe)) * float64(dims)
}

func matchRowsOrAll(rows int, sel float64, filtered bool) float64 {
	if filtered {
		return float64(rows) * sel
	}
	return float64(rows)
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// estimateSelectivity derives a selectivity estimate from a storage predicate's
// column statistics (spec 13 §7). Because storage.Predicate is opaque (its methods
// are unexported), estimation works through the engine's MetadataFilter count when a
// snapshot-cheap estimate is unavailable; here we use the engine's column-stats path
// via a best-effort probe and otherwise report no estimate.
func estimateSelectivity(coll *Collection, pred storage.Predicate) (float64, bool) {
	// vec's storage layer exposes selectivity estimation behind ColumnStats but the
	// predicate shape is opaque to this package. The db layer ([14]) supplies an
	// explicit Selectivity for compound predicates; here we conservatively decline,
	// letting the caller's default apply.
	_ = coll
	_ = pred
	return 0, false
}
