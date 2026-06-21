package obs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync/atomic"
	"time"
)

// SlowQueryRecord is one entry in the structured slow-query log (spec 18 §3.2).
// The field tags are normative so the JSON shape is stable across a Go caller and
// a downstream parser.
type SlowQueryRecord struct {
	Time              time.Time  `json:"time"`
	Collection        string     `json:"collection"`
	IndexType         string     `json:"index_type"`
	QueryID           uint64     `json:"query_id"`
	DurationMs        float64    `json:"duration_ms"`
	K                 int        `json:"k"`
	EfSearch          int        `json:"ef_search,omitempty"`
	Nprobe            int        `json:"nprobe,omitempty"`
	CandidatesVisited int        `json:"candidates_visited"`
	RerankCount       int        `json:"rerank_count"`
	FilterPresent     bool       `json:"filter_present"`
	FilterSelectiv    float64    `json:"filter_selectivity"`
	PlanShape         string     `json:"plan_shape"`
	RecallEstimate    float64    `json:"recall_estimate"`
	VectorHash        string     `json:"vector_hash"`
	FilterRepr        string     `json:"filter_repr"`
	ErrorMsg          string     `json:"error,omitempty"`
	StageDurations    StageTimes `json:"stage_durations_ms"`
}

// StageTimes is the per-stage latency breakdown in milliseconds (spec 18 §3.2).
type StageTimes struct {
	Parse     float64 `json:"parse"`
	Plan      float64 `json:"plan"`
	IndexScan float64 `json:"index_scan"`
	Rerank    float64 `json:"rerank"`
	Filter    float64 `json:"filter"`
	Assemble  float64 `json:"assemble"`
}

// SlowQueryOptions configures the slow-query log (spec 18 §3.3, §3.5).
type SlowQueryOptions struct {
	// Threshold logs any query slower than this. Zero logs every query, which is
	// only sane in combination with SampleRate well below 1.
	Threshold time.Duration
	// SampleRate logs this fraction of all queries regardless of latency, in
	// [0,1]. The threshold and the sample rate compose with OR.
	SampleRate float64
	// Redact replaces the filter text with "<redacted>" (spec 18 §3.4).
	Redact bool
	// Sink receives the records. A nil sink disables logging.
	Sink *slog.Logger
}

// SlowQueryLogger decides whether a query is logged and emits the record (spec 18
// §3). It assigns each query a monotone id and samples with a lock-free PRNG so
// the decision adds no contention to the query hot path.
type SlowQueryLogger struct {
	opts    SlowQueryOptions
	queryID atomic.Uint64
	rng     atomic.Uint64 // xorshift state for sampling
}

// NewSlowQueryLogger builds a logger. seed seeds the sampling PRNG; the caller
// passes a value from crypto/rand at startup (spec 18 §3.3). A zero seed is
// replaced with a fixed nonzero constant so the generator never sticks at zero.
func NewSlowQueryLogger(opts SlowQueryOptions, seed uint64) *SlowQueryLogger {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	l := &SlowQueryLogger{opts: opts}
	l.rng.Store(seed)
	return l
}

// NextQueryID returns the id for the next query. The executor stamps it into the
// record it builds, so the id is the same whether or not the query is logged.
func (l *SlowQueryLogger) NextQueryID() uint64 {
	return l.queryID.Add(1)
}

// ShouldLog reports whether a query of the given duration is logged (spec 18
// §3.3). A query qualifies if it crosses the threshold or wins the sample.
func (l *SlowQueryLogger) ShouldLog(d time.Duration) bool {
	if l.opts.Sink == nil {
		return false
	}
	if l.opts.Threshold > 0 && d >= l.opts.Threshold {
		return true
	}
	if l.opts.Threshold == 0 && l.opts.SampleRate <= 0 {
		return true // threshold 0 and no sampling means log everything
	}
	if l.opts.SampleRate > 0 {
		return l.sample()
	}
	return false
}

// sample draws one Bernoulli trial with probability opts.SampleRate using an
// xorshift64 generator, which is lock-free and good enough for sampling.
func (l *SlowQueryLogger) sample() bool {
	for {
		x := l.rng.Load()
		nx := x
		nx ^= nx << 13
		nx ^= nx >> 7
		nx ^= nx << 17
		if l.rng.CompareAndSwap(x, nx) {
			// Map to [0,1) using the top 53 bits for a uniform float64.
			f := float64(nx>>11) / float64(1<<53)
			return f < l.opts.SampleRate
		}
	}
}

// Log emits a record if the query qualifies (spec 18 §3). The duration field on
// the record decides; ShouldLog is applied here so the caller has one entry
// point. Redaction is applied to the filter representation before writing.
func (l *SlowQueryLogger) Log(ctx context.Context, rec SlowQueryRecord) {
	if !l.ShouldLog(time.Duration(rec.DurationMs * float64(time.Millisecond))) {
		return
	}
	if l.opts.Redact && rec.FilterPresent {
		rec.FilterRepr = "<redacted>"
	}
	l.opts.Sink.LogAttrs(ctx, slog.LevelWarn, "slow_query",
		slog.Time("time", rec.Time),
		slog.String("collection", rec.Collection),
		slog.String("index_type", rec.IndexType),
		slog.Uint64("query_id", rec.QueryID),
		slog.Float64("duration_ms", rec.DurationMs),
		slog.Int("k", rec.K),
		slog.Int("ef_search", rec.EfSearch),
		slog.Int("nprobe", rec.Nprobe),
		slog.Int("candidates_visited", rec.CandidatesVisited),
		slog.Int("rerank_count", rec.RerankCount),
		slog.Bool("filter_present", rec.FilterPresent),
		slog.Float64("filter_selectivity", rec.FilterSelectiv),
		slog.String("plan_shape", rec.PlanShape),
		slog.Float64("recall_estimate", rec.RecallEstimate),
		slog.String("vector_hash", rec.VectorHash),
		slog.String("filter_repr", rec.FilterRepr),
		slog.String("error", rec.ErrorMsg),
		slog.Group("stage_durations_ms",
			slog.Float64("parse", rec.StageDurations.Parse),
			slog.Float64("plan", rec.StageDurations.Plan),
			slog.Float64("index_scan", rec.StageDurations.IndexScan),
			slog.Float64("rerank", rec.StageDurations.Rerank),
			slog.Float64("filter", rec.StageDurations.Filter),
			slog.Float64("assemble", rec.StageDurations.Assemble),
		),
	)
}

// HashVector returns the truncated SHA-256 of a query vector's float32 bytes (spec
// 18 §3.4). The log records this, never the vector itself, so an embedding does
// not leak into the log. The caller passes the raw little-endian float32 bytes.
func HashVector(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]) // 16 hex chars, the spec's SHA-256[:16]
}
