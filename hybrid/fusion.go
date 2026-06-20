package hybrid

import (
	"math"
	"sort"
)

// DefaultRRFK is the RRF smoothing constant (spec 11 §10.2), robust across retrieval
// tasks per Cormack 2009.
const DefaultRRFK = 60.0

// RRFuse fuses ranked lists with Reciprocal Rank Fusion (spec 11 §10.2). Each input
// list is already sorted best-first; a position's fused score is the sum over lists
// of 1/(kRRF + rank), with rank 1-indexed and absent positions contributing 0. The
// result is sorted by fused score descending, ties broken by smaller position for
// determinism.
func RRFuse(lists [][]ScoredPos, kRRF float64) []ScoredPos {
	return WeightedRRFuse(lists, nil, kRRF)
}

// WeightedRRFuse is RRF with per-list weights (spec 11 §10.4). Weights default to 1.0
// and are normalized to average 1.0 so the kRRF denominator's scale is preserved;
// nil weights reduce to plain RRF.
func WeightedRRFuse(lists [][]ScoredPos, weights []float64, kRRF float64) []ScoredPos {
	if kRRF <= 0 {
		kRRF = DefaultRRFK
	}
	w := normalizeWeights(weights, len(lists))
	scores := make(map[uint32]float64)
	for li, list := range lists {
		for rank, sp := range list {
			scores[sp.Pos] += w[li] / (kRRF + float64(rank+1))
		}
	}
	return sortScored(scores)
}

// normalizeWeights returns n weights averaging 1.0. A nil or short slice is padded
// with 1.0; an all-zero slice falls back to uniform.
func normalizeWeights(weights []float64, n int) []float64 {
	out := make([]float64, n)
	sum := 0.0
	for i := 0; i < n; i++ {
		if i < len(weights) && weights[i] > 0 {
			out[i] = weights[i]
		} else {
			out[i] = 1.0
		}
		sum += out[i]
	}
	if sum == 0 {
		for i := range out {
			out[i] = 1.0
		}
		return out
	}
	scale := float64(n) / sum
	for i := range out {
		out[i] *= scale
	}
	return out
}

// FusionMethod selects the score-combination strategy (spec 11 §10.5).
type FusionMethod uint8

const (
	FusionRRF    FusionMethod = iota // rank-based reciprocal rank fusion (default)
	FusionMinMax                     // min-max normalize each list, then sum
	FusionZScore                     // z-score normalize each list, then sum
)

// Fuse combines ranked lists by the chosen method (spec 11 §10.5). RRF uses kRRF;
// the normalization methods ignore it. Weights apply to every method.
func Fuse(method FusionMethod, lists [][]ScoredPos, weights []float64, kRRF float64) []ScoredPos {
	switch method {
	case FusionMinMax:
		return normFuse(lists, weights, minMaxNormalize)
	case FusionZScore:
		return normFuse(lists, weights, zScoreNormalize)
	default:
		return WeightedRRFuse(lists, weights, kRRF)
	}
}

// normFuse applies a per-list score normalizer then sums weighted normalized scores.
func normFuse(lists [][]ScoredPos, weights []float64, norm func([]ScoredPos) map[uint32]float64) []ScoredPos {
	w := normalizeWeights(weights, len(lists))
	scores := make(map[uint32]float64)
	for li, list := range lists {
		for pos, v := range norm(list) {
			scores[pos] += w[li] * v
		}
	}
	return sortScored(scores)
}

// minMaxNormalize scales a list's scores to [0,1] by its own min and max (spec 11
// §10.5). A degenerate range maps every score to 1.0.
func minMaxNormalize(list []ScoredPos) map[uint32]float64 {
	out := make(map[uint32]float64, len(list))
	if len(list) == 0 {
		return out
	}
	lo, hi := list[0].Score, list[0].Score
	for _, sp := range list {
		if sp.Score < lo {
			lo = sp.Score
		}
		if sp.Score > hi {
			hi = sp.Score
		}
	}
	span := hi - lo
	for _, sp := range list {
		if span == 0 {
			out[sp.Pos] = 1.0
		} else {
			out[sp.Pos] = (sp.Score - lo) / span
		}
	}
	return out
}

// zScoreNormalize normalizes a list's scores to zero mean and unit variance (spec 11
// §10.5). Zero variance maps every score to 0.
func zScoreNormalize(list []ScoredPos) map[uint32]float64 {
	out := make(map[uint32]float64, len(list))
	n := float64(len(list))
	if n == 0 {
		return out
	}
	mean := 0.0
	for _, sp := range list {
		mean += sp.Score
	}
	mean /= n
	varSum := 0.0
	for _, sp := range list {
		d := sp.Score - mean
		varSum += d * d
	}
	std := math.Sqrt(varSum / n)
	for _, sp := range list {
		if std == 0 {
			out[sp.Pos] = 0
		} else {
			out[sp.Pos] = (sp.Score - mean) / std
		}
	}
	return out
}

// sortScored materializes and sorts a score map best-first, ties by smaller position.
func sortScored(scores map[uint32]float64) []ScoredPos {
	out := make([]ScoredPos, 0, len(scores))
	for pos, s := range scores {
		out = append(out, ScoredPos{Pos: pos, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Pos < out[j].Pos
	})
	return out
}

// TrimK returns the first k results, the final step of hybrid execution (spec 11
// §10.3 step 4).
func TrimK(list []ScoredPos, k int) []ScoredPos {
	if k >= 0 && len(list) > k {
		return list[:k]
	}
	return list
}
