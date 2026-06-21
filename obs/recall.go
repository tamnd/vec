package obs

// RecallSampler estimates recall in production without ground-truth labels, by
// shadow-sampling against the exact flat search (spec 18 §5.2). It picks stored
// points at random, runs the ANN query and the exact query for each, and reports
// the average intersection over k. The two searches are injected as functions so
// the sampler does not depend on the index or the executor.
type RecallSampler struct {
	opts    RecallOptions
	metrics *Metrics
}

// RecallOptions configures one sampler (spec 18 §5.2 PRAGMAs).
type RecallOptions struct {
	// Collection and Index name the series the estimate is recorded under.
	Collection string
	Index      string
	// SampleSize is how many probe points to draw per run (default 100).
	SampleSize int
	// K is the neighbor count to measure recall at (default 10).
	K int
	// AlarmThreshold fires the recall-regression alarm when the estimate drops
	// below it (spec 18 §5.3). Zero disables the alarm.
	AlarmThreshold float64
}

// SearchFunc returns the ids of the top results for a query vector. The ANN
// variant returns the index's approximate neighbors; the flat variant returns the
// exact neighbors from a brute-force scan (spec 18 §5.2 steps 2 and 3).
type SearchFunc func(query []float32, k int) ([]uint64, error)

// SampleSource yields the probe points for one run: the ids drawn at random and
// their vectors (spec 18 §5.2 step 1). The sampler does not own point storage, so
// the caller supplies the draw.
type SampleSource func(n int) (ids []uint64, vectors [][]float32, err error)

// NewRecallSampler builds a sampler that records into m. Zero SampleSize and K
// fall back to the spec defaults.
func NewRecallSampler(opts RecallOptions, m *Metrics) *RecallSampler {
	if opts.SampleSize <= 0 {
		opts.SampleSize = 100
	}
	if opts.K <= 0 {
		opts.K = 10
	}
	return &RecallSampler{opts: opts, metrics: m}
}

// RecallResult is the outcome of one sampling run.
type RecallResult struct {
	// Estimate is the mean recall@k across the sampled points, in [0,1].
	Estimate float64
	// Samples is the number of probe points that contributed (draws that failed a
	// search are skipped and do not count).
	Samples int
	// Alarmed reports whether the estimate fell below the alarm threshold.
	Alarmed bool
}

// Run executes one sampling pass (spec 18 §5.2). It draws probe points from src,
// runs ann and flat for each, and averages the per-point recall. ageSeconds is
// the freshness stamp recorded with the estimate; the caller passes the seconds
// elapsed since the previous run (the sampler holds no clock, per the no-Date.now
// constraint of the workflow harness). A run that draws nothing returns a zero
// estimate without recording, so an empty collection does not look like a recall
// collapse.
func (s *RecallSampler) Run(src SampleSource, ann, flat SearchFunc, ageSeconds float64) (RecallResult, error) {
	_, vectors, err := src(s.opts.SampleSize)
	if err != nil {
		return RecallResult{}, err
	}
	if len(vectors) == 0 {
		return RecallResult{}, nil
	}

	var sum float64
	var n int
	for _, v := range vectors {
		got, err := ann(v, s.opts.K)
		if err != nil {
			continue
		}
		truth, err := flat(v, s.opts.K)
		if err != nil {
			continue
		}
		sum += recallAtK(got, truth, s.opts.K)
		n++
	}
	if n == 0 {
		return RecallResult{}, nil
	}

	est := sum / float64(n)
	res := RecallResult{Estimate: est, Samples: n}
	if s.metrics != nil {
		s.metrics.RecordRecall(s.opts.Collection, s.opts.Index, s.opts.K, est, ageSeconds)
	}
	if s.opts.AlarmThreshold > 0 && est < s.opts.AlarmThreshold {
		res.Alarmed = true
		if s.metrics != nil {
			s.metrics.RecallAlarm(s.opts.Collection)
		}
	}
	return res, nil
}

// recallAtK is the intersection of the approximate and exact id sets over k (spec
// 18 §5.2 step 4). Each side is truncated to k first, so a search that returns
// more than k does not inflate the score. The denominator is the smaller of k and
// the truth size, so a collection with fewer than k points still scores 1.0 when
// the ANN result matches the exact result.
func recallAtK(got, truth []uint64, k int) float64 {
	if k <= 0 {
		return 0
	}
	if len(got) > k {
		got = got[:k]
	}
	if len(truth) > k {
		truth = truth[:k]
	}
	if len(truth) == 0 {
		return 0
	}
	set := make(map[uint64]struct{}, len(truth))
	for _, id := range truth {
		set[id] = struct{}{}
	}
	var hit int
	for _, id := range got {
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

// RerankAgreement is the cheaper recall proxy of spec 18 §5.4: among the final
// top-k, the fraction that was already in the pre-rerank top-k. It is computed
// from data the query pipeline already holds, so it costs nothing extra. preTopK
// is the ann ranking before reranking; finalTopK is the ranking after.
func RerankAgreement(preTopK, finalTopK []uint64, k int) float64 {
	if k <= 0 {
		return 0
	}
	if len(finalTopK) > k {
		finalTopK = finalTopK[:k]
	}
	if len(preTopK) > k {
		preTopK = preTopK[:k]
	}
	if len(finalTopK) == 0 {
		return 0
	}
	pre := make(map[uint64]struct{}, len(preTopK))
	for _, id := range preTopK {
		pre[id] = struct{}{}
	}
	var hit int
	for _, id := range finalTopK {
		if _, ok := pre[id]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(finalTopK))
}
