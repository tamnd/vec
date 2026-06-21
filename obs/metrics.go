// Package obs is the observability surface from spec 18: the metrics catalogue,
// the structured slow-query log, the flat-oracle recall sampler, and the health
// and readiness report. It is engine-agnostic. The metric updaters take plain
// values, the recall sampler takes search functions, and the health report takes
// a snapshot, so the package is testable without the storage engine and the
// server composes it by feeding it those inputs.
//
// Metrics export as Prometheus text exposition (spec 18 section 1.5: pull-based,
// no out-of-band agent). The registry here is self-contained and adds no client
// dependency; a deployment that already runs the Prometheus client wraps the
// registry in a prometheus.Collector, which is the one adapter that lives outside
// this package.
package obs

import (
	"math"
	"sort"
	"strings"
	"sync/atomic"
)

// LatencyBuckets is the upper-bound set for query latency histograms in seconds
// (spec 18 section 2.2). The buckets match the spec table exactly.
var LatencyBuckets = []float64{0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 1.0}

// Counter is a monotone counter updated without a lock on the hot path (spec 18
// section 2.10).
type Counter struct{ v atomic.Int64 }

// Add increments the counter by delta.
func (c *Counter) Add(delta int64) { c.v.Add(delta) }

// Inc increments the counter by one.
func (c *Counter) Inc() { c.v.Add(1) }

// Value returns the current count.
func (c *Counter) Value() int64 { return c.v.Load() }

// Gauge is a value that goes up and down (spec 18 section 2.5). It stores a
// float64 in an atomic uint64 bit pattern so reads and writes are lock-free.
type Gauge struct{ bits atomic.Uint64 }

// Set stores v.
func (g *Gauge) Set(v float64) { g.bits.Store(float64bits(v)) }

// Value returns the current value.
func (g *Gauge) Value() float64 { return float64frombits(g.bits.Load()) }

// Histogram is a cumulative bucketed distribution (spec 18 section 2.2). Bucket
// counts, the running sum, and the total count are atomic, so Observe takes no
// lock on the query hot path.
type Histogram struct {
	bounds  []float64
	buckets []atomic.Int64 // len(bounds)+1; last is the +Inf overflow bucket
	sum     atomic.Uint64  // float64 bits
	count   atomic.Int64
}

// NewHistogram builds a histogram over the given upper bounds, which must be
// sorted ascending. The implicit final +Inf bucket is added automatically.
func NewHistogram(bounds []float64) *Histogram {
	b := make([]float64, len(bounds))
	copy(b, bounds)
	return &Histogram{bounds: b, buckets: make([]atomic.Int64, len(b)+1)}
}

// Observe records one sample.
func (h *Histogram) Observe(v float64) {
	i := sort.SearchFloat64s(h.bounds, v)
	// SearchFloat64s returns the first index whose bound is >= v; a sample equal
	// to a bound belongs in that bound's bucket, which Prometheus counts as <=.
	if i < len(h.bounds) && h.bounds[i] < v {
		i++
	}
	h.buckets[i].Add(1)
	h.count.Add(1)
	for {
		old := h.sum.Load()
		nv := float64bits(float64frombits(old) + v)
		if h.sum.CompareAndSwap(old, nv) {
			break
		}
	}
}

// Count returns the number of observations.
func (h *Histogram) Count() int64 { return h.count.Load() }

// Sum returns the sum of all observations.
func (h *Histogram) Sum() float64 { return float64frombits(h.sum.Load()) }

// cumulative returns the cumulative bucket counts aligned with h.bounds plus the
// final +Inf entry, which is the form Prometheus exposition wants.
func (h *Histogram) cumulative() []int64 {
	out := make([]int64, len(h.buckets))
	var running int64
	for i := range h.buckets {
		running += h.buckets[i].Load()
		out[i] = running
	}
	return out
}

// labelSet is an ordered set of label name/value pairs that identifies one
// metric series. Series are keyed by their rendered label string.
type labelSet struct {
	names  []string
	values []string
}

func labels(pairs ...string) labelSet {
	ls := labelSet{}
	for i := 0; i+1 < len(pairs); i += 2 {
		ls.names = append(ls.names, pairs[i])
		ls.values = append(ls.values, pairs[i+1])
	}
	return ls
}

// key renders the label set into the deterministic {a="b",c="d"} form used both
// as the series map key and in the exposition output.
func (ls labelSet) key() string {
	if len(ls.names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := range ls.names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(ls.names[i])
		b.WriteString(`="`)
		b.WriteString(escapeLabel(ls.values[i]))
		b.WriteString(`"`)
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabel(s string) string {
	if !strings.ContainsAny(s, `\"`+"\n") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

func float64bits(f float64) uint64     { return math.Float64bits(f) }
func float64frombits(b uint64) float64 { return math.Float64frombits(b) }
