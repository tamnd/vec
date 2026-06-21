package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCounter(t *testing.T) {
	var c Counter
	c.Inc()
	c.Add(4)
	if got := c.Value(); got != 5 {
		t.Fatalf("counter = %d, want 5", got)
	}
}

func TestGauge(t *testing.T) {
	var g Gauge
	g.Set(3.5)
	if got := g.Value(); got != 3.5 {
		t.Fatalf("gauge = %v, want 3.5", got)
	}
	g.Set(-1.25)
	if got := g.Value(); got != -1.25 {
		t.Fatalf("gauge = %v, want -1.25", got)
	}
}

func TestHistogramBucketing(t *testing.T) {
	h := NewHistogram([]float64{1, 2, 5})
	for _, v := range []float64{0.5, 1, 1.5, 2, 3, 9} {
		h.Observe(v)
	}
	if h.Count() != 6 {
		t.Fatalf("count = %d, want 6", h.Count())
	}
	if math.Abs(h.Sum()-17) > 1e-9 {
		t.Fatalf("sum = %v, want 17", h.Sum())
	}
	cum := h.cumulative()
	// bounds 1,2,5,+Inf -> cumulative <=1:{0.5,1}=2, <=2:{+1.5,2}=4, <=5:{+3}=5, +Inf:{+9}=6
	want := []int64{2, 4, 5, 6}
	for i := range want {
		if cum[i] != want[i] {
			t.Fatalf("cumulative[%d] = %d, want %d (all %v)", i, cum[i], want[i], cum)
		}
	}
}

func TestHistogramConcurrent(t *testing.T) {
	h := NewHistogram(LatencyBuckets)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				h.Observe(0.003)
			}
		}()
	}
	wg.Wait()
	if h.Count() != 8000 {
		t.Fatalf("count = %d, want 8000", h.Count())
	}
	if math.Abs(h.Sum()-24) > 1e-6 {
		t.Fatalf("sum = %v, want 24", h.Sum())
	}
}

func TestRegistryTextCounterGauge(t *testing.T) {
	r := NewRegistry()
	r.Counter("vec_query_total", "Queries completed.", "collection", "docs", "status", "ok").Add(3)
	r.Gauge("vec_wal_size_bytes", "WAL size.", "collection", "docs").Set(1024)
	out := r.Text()
	for _, want := range []string{
		"# HELP vec_query_total Queries completed.",
		"# TYPE vec_query_total counter",
		`vec_query_total{collection="docs",status="ok"} 3`,
		"# TYPE vec_wal_size_bytes gauge",
		`vec_wal_size_bytes{collection="docs"} 1024`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exposition missing %q in:\n%s", want, out)
		}
	}
}

func TestRegistryHistogramText(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("vec_query_duration_seconds", "Latency.", []float64{0.001, 0.01}, "collection", "docs")
	h.Observe(0.0005)
	h.Observe(0.005)
	out := r.Text()
	for _, want := range []string{
		"# TYPE vec_query_duration_seconds histogram",
		`vec_query_duration_seconds_bucket{collection="docs",le="0.001"} 1`,
		`vec_query_duration_seconds_bucket{collection="docs",le="0.01"} 2`,
		`vec_query_duration_seconds_bucket{collection="docs",le="+Inf"} 2`,
		`vec_query_duration_seconds_count{collection="docs"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("histogram exposition missing %q in:\n%s", want, out)
		}
	}
}

func TestRegistryGetOrCreateIsStable(t *testing.T) {
	r := NewRegistry()
	a := r.Counter("vec_upsert_total", "Upserts.", "collection", "docs")
	b := r.Counter("vec_upsert_total", "Upserts.", "collection", "docs")
	if a != b {
		t.Fatal("same name and labels returned different counters")
	}
	a.Inc()
	if b.Value() != 1 {
		t.Fatalf("shared counter not shared: %d", b.Value())
	}
}

func TestEscapeLabel(t *testing.T) {
	r := NewRegistry()
	r.Counter("vec_query_total", "h", "q", `a"b\c`+"\n").Inc()
	out := r.Text()
	if !strings.Contains(out, `q="a\"b\\c\n"`) {
		t.Fatalf("label not escaped:\n%s", out)
	}
}

func TestMetricsRecordQuery(t *testing.T) {
	m := NewMetrics()
	m.RecordQuery(QueryObservation{
		Collection:        "docs",
		Index:             "hnsw",
		FilterPresent:     true,
		Status:            "ok",
		DurationSeconds:   0.004,
		CandidatesVisited: 120,
		RerankCount:       40,
		K:                 10,
		EfEffective:       64,
		FilterSelectivity: 0.3,
	})
	out := m.Registry().Text()
	for _, want := range []string{
		`vec_query_total{collection="docs",index="hnsw",status="ok"} 1`,
		"vec_query_duration_seconds_count",
		"vec_query_candidates_visited_count",
		"vec_query_filter_selectivity",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSlowQueryThreshold(t *testing.T) {
	var buf bytes.Buffer
	sink := slog.New(slog.NewJSONHandler(&buf, nil))
	l := NewSlowQueryLogger(SlowQueryOptions{Threshold: 5 * time.Millisecond, Sink: sink}, 1)

	l.Log(context.Background(), SlowQueryRecord{Collection: "docs", DurationMs: 2})
	if buf.Len() != 0 {
		t.Fatalf("fast query was logged: %s", buf.String())
	}
	l.Log(context.Background(), SlowQueryRecord{Collection: "docs", DurationMs: 8, K: 10})
	if buf.Len() == 0 {
		t.Fatal("slow query was not logged")
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line not JSON: %v", err)
	}
	if rec["msg"] != "slow_query" {
		t.Fatalf("msg = %v", rec["msg"])
	}
	if rec["collection"] != "docs" {
		t.Fatalf("collection = %v", rec["collection"])
	}
}

func TestSlowQueryRedaction(t *testing.T) {
	var buf bytes.Buffer
	sink := slog.New(slog.NewJSONHandler(&buf, nil))
	l := NewSlowQueryLogger(SlowQueryOptions{Threshold: time.Millisecond, Redact: true, Sink: sink}, 1)
	l.Log(context.Background(), SlowQueryRecord{
		Collection:    "docs",
		DurationMs:    5,
		FilterPresent: true,
		FilterRepr:    `price > 100 AND brand = "acme"`,
	})
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["filter_repr"] != "<redacted>" {
		t.Fatalf("filter not redacted: %v", rec["filter_repr"])
	}
	if rec["filter_present"] != true {
		t.Fatalf("filter_present lost: %v", rec["filter_present"])
	}
}

func TestSlowQueryNilSink(t *testing.T) {
	l := NewSlowQueryLogger(SlowQueryOptions{Threshold: time.Nanosecond}, 1)
	// Must not panic with a nil sink.
	l.Log(context.Background(), SlowQueryRecord{DurationMs: 100})
	if l.ShouldLog(time.Hour) {
		t.Fatal("ShouldLog true with nil sink")
	}
}

func TestSlowQueryQueryID(t *testing.T) {
	l := NewSlowQueryLogger(SlowQueryOptions{}, 1)
	if a, b := l.NextQueryID(), l.NextQueryID(); a != 1 || b != 2 {
		t.Fatalf("ids = %d,%d want 1,2", a, b)
	}
}

func TestSlowQuerySampleRate(t *testing.T) {
	var buf bytes.Buffer
	sink := slog.New(slog.NewJSONHandler(&buf, nil))
	// Threshold high so only sampling can fire; rate 1.0 logs every query.
	l := NewSlowQueryLogger(SlowQueryOptions{Threshold: time.Hour, SampleRate: 1.0, Sink: sink}, 12345)
	n := 0
	for i := 0; i < 50; i++ {
		buf.Reset()
		l.Log(context.Background(), SlowQueryRecord{DurationMs: 1})
		if buf.Len() > 0 {
			n++
		}
	}
	if n != 50 {
		t.Fatalf("sample rate 1.0 logged %d/50", n)
	}
}

func TestHashVector(t *testing.T) {
	h := HashVector([]byte{1, 2, 3, 4})
	if len(h) != 16 {
		t.Fatalf("hash len = %d, want 16", len(h))
	}
	if h != HashVector([]byte{1, 2, 3, 4}) {
		t.Fatal("hash not stable")
	}
	if h == HashVector([]byte{1, 2, 3, 5}) {
		t.Fatal("hash collided on different input")
	}
}

func TestRecallAtK(t *testing.T) {
	cases := []struct {
		got, truth []uint64
		k          int
		want       float64
	}{
		{[]uint64{1, 2, 3}, []uint64{1, 2, 3}, 3, 1.0},
		{[]uint64{1, 2, 9}, []uint64{1, 2, 3}, 3, 2.0 / 3.0},
		{[]uint64{7, 8, 9}, []uint64{1, 2, 3}, 3, 0.0},
		{[]uint64{1, 2, 3, 4, 5}, []uint64{1, 2, 3}, 3, 1.0}, // got truncated to k
		{[]uint64{1}, []uint64{1}, 3, 1.0},                   // fewer than k points
	}
	for i, c := range cases {
		if got := recallAtK(c.got, c.truth, c.k); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("case %d: recall = %v, want %v", i, got, c.want)
		}
	}
}

func TestRecallSamplerRun(t *testing.T) {
	m := NewMetrics()
	s := NewRecallSampler(RecallOptions{Collection: "docs", Index: "hnsw", SampleSize: 3, K: 2, AlarmThreshold: 0.9}, m)

	vecs := [][]float32{{1, 0}, {0, 1}, {1, 1}}
	src := func(n int) ([]uint64, [][]float32, error) {
		return []uint64{1, 2, 3}, vecs, nil
	}
	// Exact oracle returns {1,2}; ANN agrees on the first probe, misses on others.
	flat := func(q []float32, k int) ([]uint64, error) { return []uint64{1, 2}, nil }
	calls := 0
	ann := func(q []float32, k int) ([]uint64, error) {
		calls++
		if calls == 1 {
			return []uint64{1, 2}, nil // recall 1.0
		}
		return []uint64{1, 9}, nil // recall 0.5
	}
	res, err := s.Run(src, ann, flat, 12)
	if err != nil {
		t.Fatal(err)
	}
	// mean of {1.0, 0.5, 0.5} = 0.666...
	if math.Abs(res.Estimate-2.0/3.0) > 1e-9 {
		t.Fatalf("estimate = %v, want 0.667", res.Estimate)
	}
	if !res.Alarmed {
		t.Fatal("estimate below threshold did not alarm")
	}
	out := m.Registry().Text()
	if !strings.Contains(out, `vec_recall_estimate{collection="docs",index="hnsw",k="2"}`) {
		t.Fatalf("recall gauge missing:\n%s", out)
	}
	if !strings.Contains(out, `vec_recall_alarm_total{collection="docs"} 1`) {
		t.Fatalf("alarm counter missing:\n%s", out)
	}
}

func TestRecallSamplerEmpty(t *testing.T) {
	s := NewRecallSampler(RecallOptions{Collection: "docs"}, NewMetrics())
	src := func(n int) ([]uint64, [][]float32, error) { return nil, nil, nil }
	res, err := s.Run(src, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Estimate != 0 || res.Samples != 0 || res.Alarmed {
		t.Fatalf("empty collection gave %+v", res)
	}
}

func TestRerankAgreement(t *testing.T) {
	if got := RerankAgreement([]uint64{1, 2, 3}, []uint64{1, 2, 3}, 3); got != 1.0 {
		t.Fatalf("agreement = %v, want 1.0", got)
	}
	if got := RerankAgreement([]uint64{1, 2, 9}, []uint64{1, 2, 3}, 3); math.Abs(got-2.0/3.0) > 1e-9 {
		t.Fatalf("agreement = %v, want 0.667", got)
	}
}

func TestHealthGrade(t *testing.T) {
	th := DefaultHealthThresholds()

	notReady := th.Grade(CollectionSnapshot{WALReplayed: false, IndexLoaded: false})
	if notReady.Status != StatusNotReady {
		t.Fatalf("status = %q, want not_ready", notReady.Status)
	}

	ok := th.Grade(CollectionSnapshot{WALReplayed: true, IndexLoaded: true, RecallEstimate: 0.97, RecallEstimateAgeS: 100})
	if ok.Status != StatusOK {
		t.Fatalf("status = %q, want ok (%s)", ok.Status, ok.Reason)
	}

	staleRecall := th.Grade(CollectionSnapshot{WALReplayed: true, IndexLoaded: true, RecallEstimateAgeS: 5000})
	if staleRecall.Status != StatusDegraded || staleRecall.Reason != "recall estimate stale" {
		t.Fatalf("stale recall = %+v", staleRecall)
	}

	bigWAL := th.Grade(CollectionSnapshot{WALReplayed: true, IndexLoaded: true, WALSizeBytes: 200 << 20})
	if bigWAL.Status != StatusDegraded || bigWAL.Reason != "wal growing without checkpoint" {
		t.Fatalf("big wal = %+v", bigWAL)
	}

	frag := th.Grade(CollectionSnapshot{WALReplayed: true, IndexLoaded: true, Fragmentation: 0.7})
	if frag.Status != StatusDegraded || frag.Reason != "fragmentation high" {
		t.Fatalf("frag = %+v", frag)
	}
}

func TestBuildHealthReportRollup(t *testing.T) {
	th := DefaultHealthThresholds()
	snaps := map[string]CollectionSnapshot{
		"a": {WALReplayed: true, IndexLoaded: true, RecallEstimateAgeS: 10},
		"b": {WALReplayed: true, IndexLoaded: true, Fragmentation: 0.9}, // degraded
	}
	rep := BuildHealthReport(th, snaps, "0.4.2", 14320)
	if rep.Status != StatusDegraded {
		t.Fatalf("rollup status = %q, want degraded", rep.Status)
	}
	if !rep.Ready() {
		t.Fatal("degraded report should still be ready")
	}
	if rep.Version != "0.4.2" || rep.UptimeS != 14320 {
		t.Fatalf("report meta = %+v", rep)
	}

	snaps["c"] = CollectionSnapshot{WALReplayed: false}
	rep = BuildHealthReport(th, snaps, "0.4.2", 1)
	if rep.Status != StatusNotReady {
		t.Fatalf("rollup with not-ready collection = %q", rep.Status)
	}
	if rep.Ready() {
		t.Fatal("not_ready report must not be ready")
	}
}

func TestHealthReportJSON(t *testing.T) {
	rep := BuildHealthReport(DefaultHealthThresholds(),
		map[string]CollectionSnapshot{"docs": {WALReplayed: true, IndexLoaded: true, RecallEstimate: 0.97, RecallEstimateAgeS: 243}},
		"0.4.2", 14320)
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"status":"ok"`, `"index_loaded":true`, `"recall_estimate":0.97`, `"uptime_s":14320`, `"version":"0.4.2"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("health JSON missing %q in %s", want, s)
		}
	}
}

func TestRuntimeCollector(t *testing.T) {
	m := NewMetrics()
	c := NewRuntimeCollector(m)
	c.Collect() // seeds the GC baseline, records gauges
	c.Collect()
	out := m.Registry().Text()
	for _, want := range []string{"vec_heap_alloc_bytes", "vec_goroutines"} {
		if !strings.Contains(out, want) {
			t.Fatalf("runtime metric %q missing in:\n%s", want, out)
		}
	}
}
