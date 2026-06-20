package query

import (
	"time"

	"github.com/tamnd/vec/distance"
	"github.com/tamnd/vec/storage"
)

// PathKind is the access path an ANN search resolves to (spec 13 §8.2).
type PathKind uint8

const (
	PathFlat    PathKind = iota // brute-force exact scan (spec 13 §6.3)
	PathHNSW                    // HNSW graph search (spec 13 §6.4)
	PathIVF                     // IVF / IVF-PQ search (spec 13 §6.5)
	PathDiskANN                 // disk-resident beam search (spec 13 §6.6)
)

// String renders a PathKind for EXPLAIN.
func (p PathKind) String() string {
	switch p {
	case PathFlat:
		return "flat"
	case PathHNSW:
		return "hnsw"
	case PathIVF:
		return "ivf"
	case PathDiskANN:
		return "diskann"
	default:
		return "path?"
	}
}

// FilterStrategy is how a metadata predicate is applied relative to ANN search
// (spec 13 §8.3, spec 10 §9.1).
type FilterStrategy uint8

const (
	FilterNone FilterStrategy = iota // no predicate
	FilterPre                        // build a bitmap, restrict the index walk
	FilterIn                         // ACORN-style in-graph filtering (bitmap to index)
	FilterPost                       // search unfiltered, drop non-matching candidates
)

// String renders a FilterStrategy for EXPLAIN.
func (f FilterStrategy) String() string {
	switch f {
	case FilterPre:
		return "pre"
	case FilterIn:
		return "in"
	case FilterPost:
		return "post"
	default:
		return "none"
	}
}

// BoundQuery is the planner's input: a query already resolved against the catalog
// by the binder ([12], task 14). It names one collection, an optional metadata
// predicate, the kNN clause, and the projection. The planner consumes it; until
// the SQL frontend lands, the db layer and tests build it directly.
type BoundQuery struct {
	// Vector is the query vector (full precision). Required for a kNN query.
	Vector []float32
	// K is the LIMIT (spec 13 §4.4); the number of rows to return.
	K int
	// Metric is the distance metric resolved from the operator/opclass (spec 13 §4.3).
	Metric distance.Metric

	// Predicate is the WHERE filter over metadata columns, or nil (spec 13 §5.4).
	Predicate storage.Predicate
	// Selectivity overrides the planner's estimate when >= 0; -1 means estimate it
	// from column statistics (spec 13 §7.4).
	Selectivity float64

	// Project lists the metadata column names to return (spec 10 §13.2).
	Project []string
	// IncludeDistance asks the executor to surface the distance/score column.
	IncludeDistance bool

	// RecallTarget is the recall lower bound ef/nprobe must satisfy (spec 13 §4.5),
	// 0 falls back to the default.
	RecallTarget float64

	// EfSearch, when > 0, pins the HNSW beam width and skips recall-based selection
	// (spec 10 §4.5 per-query WITH override). NProbe does the same for IVF.
	EfSearch int
	NProbe   int
	// RerankR, when > 0, pins the rerank candidate count (spec 10 §8.2).
	RerankR int
	// AllowFlat permits a flat-scan fallback when no index matches (spec 10 §23.1).
	AllowFlat bool
	// Timeout bounds the execution; 0 uses the executor default (spec 10 §15.2).
	Timeout time.Duration
}

// PhysicalPlan is the planner's output: the fully committed execution choices the
// executor walks without re-planning (spec 10 §1.3). It is an annotation record,
// not the operator tree; the executor builds operators from it at Execute time.
type PhysicalPlan struct {
	Path        PathKind
	Filter      FilterStrategy
	K           int
	EfSearch    int
	NProbe      int
	Metric      distance.Metric
	Rerank      bool
	RerankR     int
	Project     []string
	IncludeDist bool

	// Predicate is carried through for the executor to build the filter bitmap.
	Predicate storage.Predicate
	// AllowWiden permits adaptive ef widening on post-filter exhaustion (spec 10 §1.5).
	AllowWiden bool
	// MaxEfSearch bounds adaptive widening (spec 10 §4.6).
	MaxEfSearch int
	// Timeout is the query deadline (spec 10 §15.2).
	Timeout time.Duration

	// EstCost and EstRecall are the planner's estimates, surfaced by EXPLAIN
	// (spec 13 §8.2).
	EstCost   float64
	EstRecall float64
}

// Row is one assembled result row (spec 10 §13.2): the external point id, the
// distance or fused score, and the projected metadata keyed by column name.
type Row struct {
	PointID  storage.PointID
	Distance float32
	Meta     map[string]storage.Value
}

// ResultSet is the executor's output (spec 10 §13.3). For kNN queries all rows are
// known when Execute returns, so it is a materialized slice plus the partial-result
// flags and the collected stats.
type ResultSet struct {
	Rows          []Row
	Partial       bool
	PartialReason string
	Stats         QueryStats
}
