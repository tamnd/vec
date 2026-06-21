package bench

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// writeFvecs encodes vectors in the texmex fvecs format for the loader tests.
func writeFvecs(vecs [][]float32) []byte {
	var b bytes.Buffer
	for _, v := range vecs {
		_ = binary.Write(&b, binary.LittleEndian, int32(len(v)))
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	return b.Bytes()
}

func writeFBin(vecs [][]float32) []byte {
	var b bytes.Buffer
	dim := 0
	if len(vecs) > 0 {
		dim = len(vecs[0])
	}
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(vecs)))
	_ = binary.Write(&b, binary.LittleEndian, uint32(dim))
	for _, v := range vecs {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	return b.Bytes()
}

func TestReadFvecs(t *testing.T) {
	in := [][]float32{{1, 2, 3}, {4, 5, 6}}
	got, dim, err := ReadFvecs(bytes.NewReader(writeFvecs(in)))
	if err != nil {
		t.Fatal(err)
	}
	if dim != 3 {
		t.Fatalf("dim = %d, want 3", dim)
	}
	if len(got) != 2 || got[1][2] != 6 {
		t.Fatalf("vectors = %v", got)
	}
}

func TestReadFvecsRagged(t *testing.T) {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, int32(3))
	_ = binary.Write(&b, binary.LittleEndian, []float32{1, 2, 3})
	_ = binary.Write(&b, binary.LittleEndian, int32(2))
	_ = binary.Write(&b, binary.LittleEndian, []float32{4, 5})
	_, _, err := ReadFvecs(bytes.NewReader(b.Bytes()))
	if err == nil || !strings.Contains(err.Error(), "ragged") {
		t.Fatalf("want ragged error, got %v", err)
	}
}

func TestReadFBin(t *testing.T) {
	in := [][]float32{{1, 2}, {3, 4}, {5, 6}}
	got, dim, err := ReadFBin(bytes.NewReader(writeFBin(in)))
	if err != nil {
		t.Fatal(err)
	}
	if dim != 2 || len(got) != 3 || got[2][1] != 6 {
		t.Fatalf("got dim=%d vecs=%v", dim, got)
	}
}

func TestReadIvecs(t *testing.T) {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, int32(3))
	_ = binary.Write(&b, binary.LittleEndian, []int32{7, 8, 9})
	got, k, err := ReadIvecs(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if k != 3 || got[0][2] != 9 {
		t.Fatalf("got k=%d %v", k, got)
	}
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]string{
		"/data/sift_base.fvecs": "fvecs",
		"q.bvecs":               "bvecs",
		"gt.ivecs":              "ivecs",
		"base.fbin":             "fbin",
		"x.u8bin":               "u8bin",
		"gt.ibin":               "ibin",
		"unknown.dat":           "",
	}
	for path, want := range cases {
		if got := DetectFormat(path); got != want {
			t.Fatalf("DetectFormat(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRecallAt(t *testing.T) {
	cases := []struct {
		result, truth []uint32
		k             int
		want          float64
	}{
		{[]uint32{1, 2, 3}, []uint32{1, 2, 3}, 3, 1.0},
		{[]uint32{1, 2, 9}, []uint32{1, 2, 3}, 3, 2.0 / 3.0},
		{[]uint32{7, 8, 9}, []uint32{1, 2, 3}, 3, 0.0},
		{[]uint32{1, 2, 3, 4, 5}, []uint32{1, 2, 3}, 3, 1.0}, // result over-returns
		{[]uint32{5}, []uint32{5}, 10, 1.0},                  // fewer than k truth ids
	}
	for i, c := range cases {
		if got := RecallAt(c.result, c.truth, c.k); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("case %d: recall = %v, want %v", i, got, c.want)
		}
	}
}

func TestMeanRecall(t *testing.T) {
	results := [][]uint32{{1, 2}, {3, 9}}
	truth := [][]uint32{{1, 2}, {3, 4}}
	got := MeanRecall(results, truth, 2)
	// (1.0 + 0.5) / 2 = 0.75
	if math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("mean recall = %v, want 0.75", got)
	}
}

func TestLatencyPercentiles(t *testing.T) {
	r := NewLatencyRecorder(100)
	for i := 1; i <= 100; i++ {
		r.Record(time.Duration(i) * time.Microsecond)
	}
	p := r.Percentiles()
	if p.Count != 100 {
		t.Fatalf("count = %d", p.Count)
	}
	// nearest-rank: p50 = ceil(0.5*100)=50th sample = 50us; p99 = 99us; max = 100us
	if p.P50 != 50 || p.P99 != 99 || p.Max != 100 {
		t.Fatalf("p50=%d p99=%d max=%d", p.P50, p.P99, p.Max)
	}
	if math.Abs(p.Mean-50.5) > 1e-9 {
		t.Fatalf("mean = %v, want 50.5", p.Mean)
	}
}

func TestLatencyEmpty(t *testing.T) {
	r := NewLatencyRecorder(0)
	if p := r.Percentiles(); p.Count != 0 || p.P99 != 0 {
		t.Fatalf("empty recorder = %+v", p)
	}
}

func TestRunSweep(t *testing.T) {
	// Two query vectors; truth is the exact neighbor set per query. The searcher
	// returns the exact answer once effort is high enough and a wrong answer below.
	queries := [][]float32{{0, 0}, {1, 1}}
	truth := [][]uint32{{10, 11}, {20, 21}}
	s := SearcherFunc(func(ctx context.Context, q []float32, k, effort int) ([]uint32, error) {
		if effort >= 40 {
			if q[0] == 0 {
				return []uint32{10, 11}, nil
			}
			return []uint32{20, 21}, nil
		}
		return []uint32{0, 0}, nil // misses everything
	})
	pts, err := RunSweep(context.Background(), s, queries, truth, SweepConfig{
		Param:       "ef_search",
		Values:      []int{10, 40},
		K:           2,
		Concurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("points = %d", len(pts))
	}
	if pts[0].Recall10 != 0 {
		t.Fatalf("low-effort recall = %v, want 0", pts[0].Recall10)
	}
	if pts[1].Recall10 != 1.0 {
		t.Fatalf("high-effort recall = %v, want 1.0", pts[1].Recall10)
	}
	if pts[1].Param != "ef_search" || pts[1].Value != 40 {
		t.Fatalf("point meta = %+v", pts[1])
	}
	if pts[1].QPS <= 0 {
		t.Fatalf("qps = %v", pts[1].QPS)
	}
}

func TestRunSweepError(t *testing.T) {
	boom := errors.New("search failed")
	s := SearcherFunc(func(ctx context.Context, q []float32, k, effort int) ([]uint32, error) {
		return nil, boom
	})
	_, err := RunSweep(context.Background(), s, [][]float32{{1}}, [][]uint32{{1}}, SweepConfig{Values: []int{10}})
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
}

func TestResultJSONAndTSV(t *testing.T) {
	r := &Result{
		Meta:  Meta{VecVersion: "0.5.2", Dataset: "sift1m", Index: "hnsw", K: 10, Dimension: 128},
		Build: Build{DurationSec: 42.3, PeakRSSMB: 1850, IndexSizeBytes: 524288000},
		Sweep: []SweepPoint{{Param: "ef_search", Value: 80, Recall10: 0.9513, QPS: 18420, P50us: 48, P99us: 105, MaxUs: 2100}},
	}
	var jb bytes.Buffer
	if err := r.WriteJSON(&jb); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"vec_version": "0.5.2"`, `"recall10": 0.9513`, `"duration_sec": 42.3`} {
		if !strings.Contains(jb.String(), want) {
			t.Fatalf("JSON missing %q in:\n%s", want, jb.String())
		}
	}
	var tb bytes.Buffer
	if err := r.WriteTSV(&tb); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(tb.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("tsv lines = %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "param\tvalue\trecall10") {
		t.Fatalf("tsv header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "ef_search\t80\t0.9513") {
		t.Fatalf("tsv row = %q", lines[1])
	}
}

func TestQPSAtRecall(t *testing.T) {
	r := &Result{Sweep: []SweepPoint{
		{Recall10: 0.90, QPS: 30000},
		{Recall10: 0.95, QPS: 18000},
		{Recall10: 0.97, QPS: 13000},
	}}
	got, ok := r.QPSAtRecall(0.95)
	if !ok || got != 18000 {
		t.Fatalf("qps at recall 0.95 = %v ok=%v, want 18000", got, ok)
	}
	if _, ok := r.QPSAtRecall(0.999); ok {
		t.Fatal("expected no point at recall 0.999")
	}
}

func TestGateHigherBetter(t *testing.T) {
	g := Gate{Name: "hnsw_sift1m_qps", Metric: "qps", Baseline: 18600, Tolerance: 0.05, Direction: HigherBetter, Level: Blocking}
	if r := g.Evaluate(18000); !r.Passed {
		t.Fatalf("18000 within 5%% of 18600 should pass, limit %v", r.Limit)
	}
	r := g.Evaluate(16900)
	if r.Passed {
		t.Fatal("16900 is below the 5% limit, should fail")
	}
	if !r.Blocking() {
		t.Fatal("failed blocking gate should block")
	}
	if !strings.Contains(r.Alarm(), "PERF REGRESSION") {
		t.Fatalf("alarm = %q", r.Alarm())
	}
}

func TestGateLowerBetter(t *testing.T) {
	g := Gate{Name: "hnsw_sift1m_p99", Metric: "p99", Baseline: 100, Tolerance: 0.10, Direction: LowerBetter, Level: Blocking}
	if r := g.Evaluate(108); !r.Passed {
		t.Fatalf("108us within 110%% of 100us should pass")
	}
	if r := g.Evaluate(120); r.Passed {
		t.Fatal("120us exceeds the 110us limit, should fail")
	}
}

func TestGateWarningDoesNotBlock(t *testing.T) {
	g := Gate{Name: "build", Baseline: 40, Tolerance: 0.15, Direction: LowerBetter, Level: Warning}
	r := g.Evaluate(50) // fails
	if r.Passed {
		t.Fatal("50 exceeds 46 limit, should fail")
	}
	if r.Blocking() {
		t.Fatal("failed warning gate must not block")
	}
}

func TestEvaluateGates(t *testing.T) {
	gates := []Gate{
		{Name: "qps", Baseline: 18000, Tolerance: 0.05, Direction: HigherBetter, Level: Blocking},
		{Name: "p99", Baseline: 100, Tolerance: 0.10, Direction: LowerBetter, Level: Blocking},
		{Name: "absent", Baseline: 1, Tolerance: 0.1, Direction: HigherBetter, Level: Blocking},
	}
	results, blocking := EvaluateGates(gates, map[string]float64{"qps": 19000, "p99": 130})
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (absent skipped)", len(results))
	}
	if !blocking {
		t.Fatal("p99 regression should set blocking")
	}
}
