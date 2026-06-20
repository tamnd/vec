// Package index implements vec's vector index access paths behind one SPI: the
// flat brute-force baseline and the HNSW graph (spec 07). Every index operates on
// positions (dense u32 internal ids, spec 02 §3) and returns candidates sorted by
// distance, so the query planner selects an access path without forking call sites.
//
// The spec layout (spec 07 §8) maps the graph onto off-GC mmap-backed arrays for
// near-instant recovery. That layout is wired to the pager in the storage-engine
// slice; this package ships the complete, correct heap-backed graph and a blob
// serialization seam (Persist/Recover over a PageStore), the same ship-the-correct-
// path-then-wire-throughput discipline used for the WAL and distance tiers.
package index

import (
	"context"
	"errors"

	"github.com/tamnd/vec/distance"
	"github.com/tamnd/vec/quant"
)

// Metric is the distance metric an index ranks by (spec 07 §1.3).
type Metric = distance.Metric

// Codec is the quantization codec an index uses for navigation codes; nil means
// full-precision fp32 (spec 07 §10, spec 09).
type Codec = quant.Quantizer

// Errors surfaced by the SPI (spec 07 §1.6).
var (
	// ErrIndexCorrupt is returned when an index detects unrecoverable corruption;
	// the engine surfaces it as a hard error requiring REINDEX.
	ErrIndexCorrupt = errors.New("index: corrupt, REINDEX required")
	// ErrIndexMemoryExceeded is returned by Insert when the configured memory budget
	// is exceeded (spec 07 §13.4).
	ErrIndexMemoryExceeded = errors.New("index: memory budget exceeded")
	// ErrClosed is returned when a method is called after Close.
	ErrClosed = errors.New("index: closed")
	// ErrDimMismatch is returned when a vector's length does not match the index dim.
	ErrDimMismatch = errors.New("index: vector dimension mismatch")
	// ErrBadParams is returned when build/config parameters are invalid.
	ErrBadParams = errors.New("index: invalid parameters")
)

// Candidate is one result from Search: a position and its distance to the query
// under the index metric (smaller is closer; Dot/IP and Cosine are oriented so the
// search heap stays a min-heap, spec 07 §4.2).
type Candidate struct {
	Position uint32
	Distance float32
}

// Bitmap is a snapshot-visible set of allowed positions for filtered search
// (spec 07 §11). nil means no predicate.
type Bitmap interface {
	// Contains reports whether pos passes the filter.
	Contains(pos uint32) bool
	// Count returns the number of positions in the set, for selectivity estimation
	// (spec 07 §11.3).
	Count() int
}

// VectorStore fetches full-precision vectors by position for rerank and graph
// navigation (spec 07 §10.4). The storage engine implements it; tests use an
// in-memory store.
type VectorStore interface {
	// Vector returns the fp32 vector at pos. The slice is read-only to the caller.
	Vector(pos uint32) []float32
}

// PageStore is the persistence seam (spec 07 §9). The pager implements it; an
// index serializes its whole state to one blob through PutBlob and reads it back
// through GetBlob. Page-granular dirty tracking is a later storage-engine slice.
type PageStore interface {
	PutBlob(b []byte) error
	GetBlob() ([]byte, error)
}

// BuildParams carries construction-time knobs (spec 07 §1.3).
type BuildParams struct {
	M              int     // max neighbors per upper layer (default 16)
	M0             int     // base-layer degree (default 2*M)
	EfConstruction int     // candidate pool size during build (default 200)
	ML             float64 // level multiplier (default 1/ln(M))
	Seed           int64   // reproducible level draws (0 = derive a fixed seed)
	Codec          Codec   // navigation codec (nil = fp32)
	Metric         Metric  // distance metric
	NaiveSelect    bool    // use naive kNN selection instead of the heuristic
}

// SearchParams carries query-time knobs (spec 07 §1.3, spec 08 §5.5, §9.5).
type SearchParams struct {
	EfSearch      int  // HNSW candidate pool size (default 50)
	MaxCandidates int  // hard cap before rerank (default 2*k)
	UseRerank     bool // rerank top candidates with full-precision vectors
	RerankFactor  int  // rerank this many * k candidates (default 3)
	NProbe        int  // IVF/SPANN: lists to probe (default 16, spec 08 §5.3)
	BeamWidth     int  // DiskANN: beam width (default 64, spec 08 §9.5)
}

// IndexStats is a snapshot of index counters (spec 07 §1.3).
type IndexStats struct {
	NodeCount        int64
	TombstoneCount   int64
	LayerHistogram   []int64
	EntrypointPos    uint32
	DistComputations int64
	SearchCount      int64
	MemoryBytes      int64
}

// Index is the normative SPI every ANN (and flat) index implements (spec 07 §1.2).
type Index interface {
	Build(ctx context.Context, positions []uint32, vectorAt func(uint32) []float32, params BuildParams) error
	Close() error
	Insert(pos uint32, vec []float32) error
	Delete(pos uint32) error
	Search(ctx context.Context, query []float32, k int, filter Bitmap, params SearchParams) ([]Candidate, error)
	Persist(ps PageStore) error
	Recover(ps PageStore) error
	Stats() IndexStats
	MemoryBytes() int64
}

// metricDistance returns the ranking distance for a metric: a function where a
// smaller value means closer (spec 07 §4.2). L2 ranks by squared distance (same
// order, no sqrt); Cosine by 1-cos; Dot/IP by the negated inner product so the
// most-similar vector sorts smallest.
func metricDistance(m Metric) func(a, b []float32) float32 {
	switch m {
	case distance.Cosine:
		return distance.CosineDistanceFloat32
	case distance.Dot:
		return func(a, b []float32) float32 { return -distance.DotFloat32(a, b) }
	default: // L2, L2Squared
		return distance.L2SquaredFloat32
	}
}
