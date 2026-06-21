package bench

import (
	"sort"
	"sync"
	"time"
)

// LatencyRecorder collects per-query latencies and reports percentiles with
// microsecond resolution (spec 20 §2.3). It is a plain sorted-sample recorder, not
// a bucketed HdrHistogram: a benchmark run holds every sample in memory anyway
// (10k to a few million queries), and exact sorted percentiles avoid the bucket
// quantization error an HdrHistogram trades for bounded memory. It is safe for
// concurrent Record from the load-generator goroutines.
type LatencyRecorder struct {
	mu      sync.Mutex
	samples []int64 // microseconds
}

// NewLatencyRecorder returns a recorder sized for an expected sample count; the
// hint only presizes the backing slice and does not cap it.
func NewLatencyRecorder(hint int) *LatencyRecorder {
	if hint < 0 {
		hint = 0
	}
	return &LatencyRecorder{samples: make([]int64, 0, hint)}
}

// Record adds one latency in microseconds.
func (r *LatencyRecorder) Record(d time.Duration) {
	us := d.Microseconds()
	r.mu.Lock()
	r.samples = append(r.samples, us)
	r.mu.Unlock()
}

// Count returns the number of recorded samples.
func (r *LatencyRecorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}

// Percentiles is the latency summary in microseconds (spec 20 §2.3).
type Percentiles struct {
	P50   int64
	P95   int64
	P99   int64
	P999  int64
	P9999 int64
	Max   int64
	Mean  float64
	Count int
}

// Percentiles returns the latency summary. It sorts a copy of the samples, so the
// recorder stays usable afterward. An empty recorder returns a zero summary.
func (r *LatencyRecorder) Percentiles() Percentiles {
	r.mu.Lock()
	sorted := append([]int64(nil), r.samples...)
	r.mu.Unlock()
	if len(sorted) == 0 {
		return Percentiles{}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum int64
	for _, v := range sorted {
		sum += v
	}
	return Percentiles{
		P50:   quantile(sorted, 0.50),
		P95:   quantile(sorted, 0.95),
		P99:   quantile(sorted, 0.99),
		P999:  quantile(sorted, 0.999),
		P9999: quantile(sorted, 0.9999),
		Max:   sorted[len(sorted)-1],
		Mean:  float64(sum) / float64(len(sorted)),
		Count: len(sorted),
	}
}

// quantile returns the p-quantile of an ascending slice using the
// nearest-rank method: rank = ceil(p*n), clamped to [1,n]. Nearest-rank is the
// convention HdrHistogram and the ANN-Benchmarks tooling report, so vec numbers
// line up with published ones.
func quantile(sorted []int64, p float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := int(ceil(p * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

func ceil(f float64) float64 {
	i := float64(int64(f))
	if f > i {
		return i + 1
	}
	return i
}
