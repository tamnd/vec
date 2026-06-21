// Package verify holds vec's correctness machinery from spec 21: the recall
// oracle, the reference model, the conformance and metamorphic drivers, and the
// fault-injecting VFS used for crash testing.
//
// The package is engine-agnostic. The oracle works on plain [][]float32, the
// reference model and the conformance driver speak a small Collection interface
// that the real DB adapts to in its own tests, and the fault VFS wraps the
// vfs.FS seam. That keeps the verification logic importable and exercised on its
// own, the same way the bench and obs packages are, and lets a test wire the
// real engine in by supplying the interface.
//
// The governing idea of spec 21 is that the dangerous machinery (WAL, MVCC, ANN
// graph mutation) is verified above a seam against a trivial oracle: flat
// brute-force search for recall, and an in-memory reference for exactness. Both
// oracles must be obviously correct, so they are kept as simple as possible even
// where that is slow.
package verify

import (
	"sort"

	"github.com/tamnd/vec/distance"
)

// FlatSearch returns the exact top-k positions for query q over vecs using the
// given metric (spec 21 §2.1). It is the ground-truth oracle for recall: it must
// be correct, it need not be fast. Ties in distance break by position so the
// result is deterministic for a deterministic dataset.
func FlatSearch(vecs [][]float32, q []float32, k int, metric distance.Metric) []int64 {
	if k <= 0 || len(vecs) == 0 {
		return nil
	}
	type scored struct {
		pos  int64
		dist float32
	}
	all := make([]scored, 0, len(vecs))
	for pos, v := range vecs {
		all = append(all, scored{int64(pos), MetricDistance(metric, q, v)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].dist != all[j].dist {
			return all[i].dist < all[j].dist
		}
		return all[i].pos < all[j].pos
	})
	if k > len(all) {
		k = len(all)
	}
	out := make([]int64, k)
	for i := 0; i < k; i++ {
		out[i] = all[i].pos
	}
	return out
}

// MeasureRecall is recall@k between an ANN result and the flat ground truth
// (spec 21 §2.2): the fraction of the ground-truth ids that appear in the ANN
// result. Both arguments are id sets (positions or point ids), compared as sets,
// so distance ties do not matter. An empty ground truth scores 1.0.
func MeasureRecall(ann, flat []int64) float64 {
	if len(flat) == 0 {
		return 1.0
	}
	hit := make(map[int64]struct{}, len(flat))
	for _, p := range flat {
		hit[p] = struct{}{}
	}
	var n int
	for _, p := range ann {
		if _, ok := hit[p]; ok {
			n++
		}
	}
	return float64(n) / float64(len(flat))
}

// MeasureRecallSet averages MeasureRecall over a set of queries already paired
// with their ground truth (spec 21 §16.2 MeasureRecallSet). The two slices are
// aligned by index; a length mismatch is bounded by the shorter one.
func MeasureRecallSet(ann, flat [][]int64) float64 {
	n := len(ann)
	if len(flat) < n {
		n = len(flat)
	}
	if n == 0 {
		return 1.0
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += MeasureRecall(ann[i], flat[i])
	}
	return sum / float64(n)
}

// MetricDistance returns a ranking distance between two fp32 vectors under the
// metric. It uses the scalar kernels in the distance package, which are the
// correctness oracle for the SIMD tiers, so the recall oracle and the engine
// agree on what "nearest" means. L2 and L2Squared rank identically, so the
// oracle uses the squared form for both and skips the square root.
func MetricDistance(metric distance.Metric, a, b []float32) float32 {
	switch metric {
	case distance.Cosine:
		return distance.CosineDistanceFloat32(a, b)
	case distance.Dot:
		// Larger inner product is more similar, so negate for a distance sense.
		return -distance.DotFloat32(a, b)
	default:
		return distance.L2SquaredFloat32(a, b)
	}
}
