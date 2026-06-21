package bench

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Searcher runs one ANN query at a given effort and returns the top-k neighbor
// ids, in result order (spec 20 §7.3). The harness injects it so the sweep does
// not depend on the index: the CLI passes a closure over the real index Search,
// and a test passes an exact or a faked search. effort is the swept value, an
// ef_search for HNSW or an nprobe for IVF.
type Searcher interface {
	Search(ctx context.Context, query []float32, k, effort int) ([]uint32, error)
}

// SearcherFunc adapts a function to a Searcher.
type SearcherFunc func(ctx context.Context, query []float32, k, effort int) ([]uint32, error)

// Search calls f.
func (f SearcherFunc) Search(ctx context.Context, query []float32, k, effort int) ([]uint32, error) {
	return f(ctx, query, k, effort)
}

// SweepPoint is one row of a parameter sweep: a single effort value with the
// recall and latency it produced (spec 20 §7.3). The JSON tags match the result
// schema of spec §7.6.
type SweepPoint struct {
	Param     string  `json:"param"`
	Value     int     `json:"value"`
	Recall10  float64 `json:"recall10"`
	QPS       float64 `json:"qps"`
	P50us     int64   `json:"p50us"`
	P95us     int64   `json:"p95us"`
	P99us     int64   `json:"p99us"`
	P999us    int64   `json:"p999us"`
	P9999us   int64   `json:"p9999us"`
	MaxUs     int64   `json:"max_us"`
	CPUPct    float64 `json:"cpu_pct"`
	GCPauseUs int64   `json:"gc_pause_us"`
}

// SweepConfig drives RunSweep (spec 20 §7.1, §7.3).
type SweepConfig struct {
	// Param is the effort parameter name recorded on each point ("ef_search" or
	// "nprobe").
	Param string
	// Values is the swept effort ladder.
	Values []int
	// K is the neighbor count to request and to measure recall at (default 10).
	K int
	// Concurrency is the number of load-generator goroutines (spec §7.5). One is
	// the ANN-Benchmarks single-thread protocol (spec §4.3); higher values measure
	// throughput under load.
	Concurrency int
}

// RunSweep runs the full query set at each effort value and returns one SweepPoint
// per value (spec 20 §7.3). For each value it issues every query, records latency
// and recall, and computes QPS as queries over wall-clock. queries and truth are
// aligned per query; truth holds the ground-truth neighbor ids.
//
// The walltime QPS is the whole-set elapsed time, which at concurrency 1 is the
// ANN-Benchmarks single-thread number and at higher concurrency is the saturated
// throughput. Latency is recorded per query regardless of concurrency.
func RunSweep(ctx context.Context, s Searcher, queries [][]float32, truth [][]uint32, cfg SweepConfig) ([]SweepPoint, error) {
	k := cfg.K
	if k <= 0 {
		k = 10
	}
	conc := cfg.Concurrency
	if conc <= 0 {
		conc = 1
	}
	points := make([]SweepPoint, 0, len(cfg.Values))
	for _, effort := range cfg.Values {
		pt, err := runOne(ctx, s, queries, truth, cfg.Param, effort, k, conc)
		if err != nil {
			return nil, err
		}
		points = append(points, pt)
	}
	return points, nil
}

// runOne runs the query set once at a fixed effort. It fans the queries across
// conc goroutines pulling from a shared atomic cursor (round-robin over the pool,
// spec §7.5) so each goroutine visits different vectors and cache-hit bias is
// reduced. Recall is computed from the stored per-query results after the run.
func runOne(ctx context.Context, s Searcher, queries [][]float32, truth [][]uint32, param string, effort, k, conc int) (SweepPoint, error) {
	n := len(queries)
	results := make([][]uint32, n)
	rec := NewLatencyRecorder(n)

	var cursor atomic.Int64
	var firstErr atomic.Value // error
	var wg sync.WaitGroup

	start := time.Now()
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(cursor.Add(1)) - 1
				if i >= n {
					return
				}
				if ctx.Err() != nil {
					return
				}
				qstart := time.Now()
				ids, err := s.Search(ctx, queries[i], k, effort)
				rec.Record(time.Since(qstart))
				if err != nil {
					firstErr.CompareAndSwap(nil, errBox{err})
					return
				}
				results[i] = ids
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if v := firstErr.Load(); v != nil {
		return SweepPoint{}, v.(errBox).err
	}

	p := rec.Percentiles()
	qps := 0.0
	if elapsed > 0 {
		qps = float64(n) / elapsed.Seconds()
	}
	return SweepPoint{
		Param:    param,
		Value:    effort,
		Recall10: MeanRecall(results, truth, k),
		QPS:      qps,
		P50us:    p.P50,
		P95us:    p.P95,
		P99us:    p.P99,
		P999us:   p.P999,
		P9999us:  p.P9999,
		MaxUs:    p.Max,
	}, nil
}

// errBox lets an error travel through atomic.Value, which needs a concrete type.
type errBox struct{ err error }
