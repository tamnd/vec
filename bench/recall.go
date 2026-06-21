package bench

// RecallAt is recall@k for one query (spec 20 §2.1, §7.4): the fraction of the k
// ground-truth neighbors that appear in the k returned results. Both sides are
// truncated to k first. The denominator is the smaller of k and the truth length,
// so a query whose ground truth holds fewer than k ids still scores in [0,1]
// instead of being penalized for missing ids that do not exist.
func RecallAt(result, truth []uint32, k int) float64 {
	if k <= 0 {
		return 0
	}
	if len(truth) > k {
		truth = truth[:k]
	}
	if len(result) > k {
		result = result[:k]
	}
	if len(truth) == 0 {
		return 0
	}
	set := make(map[uint32]struct{}, len(truth))
	for _, id := range truth {
		set[id] = struct{}{}
	}
	hit := 0
	for _, id := range result {
		if _, ok := set[id]; ok {
			hit++
		}
	}
	denom := k
	if len(truth) < denom {
		denom = len(truth)
	}
	return float64(hit) / float64(denom)
}

// MeanRecall averages RecallAt over a query set (spec 20 §2.1). results and truth
// are aligned per query; a shorter slice bounds the count so a mismatch does not
// panic.
func MeanRecall(results, truth [][]uint32, k int) float64 {
	n := len(results)
	if len(truth) < n {
		n = len(truth)
	}
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += RecallAt(results[i], truth[i], k)
	}
	return sum / float64(n)
}
