package obs

// Metric names from the spec 18 section 2 catalogue. They are exported so the
// server, the tests, and any external alerting rule reference the same strings.
const (
	MQueryDuration          = "vec_query_duration_seconds"
	MQueryTotal             = "vec_query_total"
	MQueryCandidatesVisited = "vec_query_candidates_visited"
	MQueryRerankCount       = "vec_query_rerank_count"
	MQueryK                 = "vec_query_k"
	MQueryEfEffective       = "vec_query_ef_effective"
	MQueryNprobeEffective   = "vec_query_nprobe_effective"
	MQueryFilterSelectivity = "vec_query_filter_selectivity"
	MPlanCacheHits          = "vec_query_plan_cache_hits_total"
	MPlanCacheMisses        = "vec_query_plan_cache_misses_total"
	MHybridRRFFused         = "vec_query_hybrid_rrf_fused_total"

	MRecallEstimate        = "vec_recall_estimate"
	MRecallShadowQueries   = "vec_recall_shadow_queries_total"
	MRecallRerankAgreement = "vec_recall_rerank_agreement"
	MRecallEstimateAge     = "vec_recall_estimate_age_seconds"
	MRecallAlarmTotal      = "vec_recall_alarm_total"

	MIndexBuildDuration   = "vec_index_build_duration_seconds"
	MIndexBuildProgress   = "vec_index_build_progress"
	MIndexBuildActive     = "vec_index_build_active"
	MIndexInsertDuration  = "vec_index_insert_duration_seconds"
	MIndexDeleteTombstone = "vec_index_delete_tombstone_total"

	MWALSizeBytes       = "vec_wal_size_bytes"
	MCheckpointDuration = "vec_checkpoint_duration_seconds"
	MCheckpointTotal    = "vec_checkpoint_total"
	MSegmentCount       = "vec_segment_count"
	MFragmentationRatio = "vec_fragmentation_ratio"
	MFileSizeBytes      = "vec_file_size_bytes"
	MPageCacheSizeBytes = "vec_page_cache_size_bytes"
	MPageCacheHits      = "vec_page_cache_hits_total"
	MPageCacheMisses    = "vec_page_cache_misses_total"

	MWriterQueueDepth   = "vec_writer_queue_depth"
	MWriterWaitDuration = "vec_writer_wait_duration_seconds"
	MUpsertTotal        = "vec_upsert_total"
	MDeleteTotal        = "vec_delete_total"

	MGCPauseDuration = "vec_gc_pause_duration_seconds"
	MHeapAllocBytes  = "vec_heap_alloc_bytes"
	MGoroutines      = "vec_goroutines"
)

// Metrics is the engine-facing recorder. It wraps a Registry and offers typed
// methods for the hot paths so callers do not repeat metric names and label keys
// at every call site (spec 18 section 2.10). The same Metrics is shared across
// the whole DB; the library returns it from db.Metrics and the server mounts the
// same instance on /metrics, so there is one set of counters and no double count.
type Metrics struct {
	reg *Registry
}

// NewMetrics builds a Metrics over a fresh registry.
func NewMetrics() *Metrics {
	return &Metrics{reg: NewRegistry()}
}

// Registry returns the underlying registry, for exposition and for the
// prometheus.Collector adapter a deployment may wrap around it.
func (m *Metrics) Registry() *Registry { return m.reg }

// QueryObservation is the set of values one completed query contributes to the
// metrics (spec 18 section 2.2). Zero-valued optional fields are skipped.
type QueryObservation struct {
	Collection        string
	Index             string
	FilterPresent     bool
	Status            string // ok / error / timeout
	DurationSeconds   float64
	CandidatesVisited int
	RerankCount       int
	K                 int
	EfEffective       int
	NprobeEffective   int
	FilterSelectivity float64 // -1 if unknown
}

// RecordQuery folds one query's observation into the metrics. It is the single
// call the executor makes when a query finishes.
func (m *Metrics) RecordQuery(o QueryObservation) {
	filt := "false"
	if o.FilterPresent {
		filt = "true"
	}
	m.reg.Histogram(MQueryDuration, "End-to-end query latency in seconds.", LatencyBuckets,
		"collection", o.Collection, "index", o.Index, "filter", filt).Observe(o.DurationSeconds)
	status := o.Status
	if status == "" {
		status = "ok"
	}
	m.reg.Counter(MQueryTotal, "Queries completed.",
		"collection", o.Collection, "index", o.Index, "status", status).Inc()
	m.reg.Histogram(MQueryCandidatesVisited, "Candidates visited per query.", candidateBuckets,
		"collection", o.Collection, "index", o.Index).Observe(float64(o.CandidatesVisited))
	if o.RerankCount > 0 {
		m.reg.Histogram(MQueryRerankCount, "Full-precision rerank candidates per query.", candidateBuckets,
			"collection", o.Collection, "index", o.Index).Observe(float64(o.RerankCount))
	}
	if o.K > 0 {
		m.reg.Histogram(MQueryK, "Requested k values.", kBuckets,
			"collection", o.Collection).Observe(float64(o.K))
	}
	if o.EfEffective > 0 {
		m.reg.Histogram(MQueryEfEffective, "Effective ef_search used.", candidateBuckets,
			"collection", o.Collection, "index", o.Index).Observe(float64(o.EfEffective))
	}
	if o.NprobeEffective > 0 {
		m.reg.Histogram(MQueryNprobeEffective, "Effective nprobe used.", candidateBuckets,
			"collection", o.Collection, "index", o.Index).Observe(float64(o.NprobeEffective))
	}
	if o.FilterPresent && o.FilterSelectivity >= 0 {
		m.reg.Histogram(MQueryFilterSelectivity, "Fraction of points passing the filter.", fractionBuckets,
			"collection", o.Collection).Observe(o.FilterSelectivity)
	}
}

// PlanCacheHit and PlanCacheMiss record the plan cache outcome (spec 18 §2.2).
func (m *Metrics) PlanCacheHit(collection string) {
	m.reg.Counter(MPlanCacheHits, "Plan cache hits.", "collection", collection).Inc()
}

func (m *Metrics) PlanCacheMiss(collection string) {
	m.reg.Counter(MPlanCacheMisses, "Plan cache misses.", "collection", collection).Inc()
}

// RecordRecall records a recall estimate and its freshness (spec 18 §2.3).
func (m *Metrics) RecordRecall(collection, index string, k int, estimate, ageSeconds float64) {
	kl := itoa(k)
	m.reg.Gauge(MRecallEstimate, "Last recall@k estimate.",
		"collection", collection, "index", index, "k", kl).Set(estimate)
	m.reg.Gauge(MRecallEstimateAge, "Seconds since the last recall estimate.",
		"collection", collection, "index", index, "k", kl).Set(ageSeconds)
	m.reg.Counter(MRecallShadowQueries, "Shadow queries run by the sampler.",
		"collection", collection).Inc()
}

// RecallAlarm increments the recall regression counter (spec 18 §5.3).
func (m *Metrics) RecallAlarm(collection string) {
	m.reg.Counter(MRecallAlarmTotal, "Recall regression alarms fired.",
		"collection", collection).Inc()
}

// RecordUpsert and RecordDelete record write outcomes (spec 18 §2.7).
func (m *Metrics) RecordUpsert(collection, status string) {
	m.reg.Counter(MUpsertTotal, "Point upserts.", "collection", collection, "status", status).Inc()
}

func (m *Metrics) RecordDelete(collection, status string) {
	m.reg.Counter(MDeleteTotal, "Point deletes.", "collection", collection, "status", status).Inc()
}

// SetWALSize, SetFileSize, SetFragmentation record storage gauges (spec 18 §2.5).
func (m *Metrics) SetWALSize(collection string, bytes int64) {
	m.reg.Gauge(MWALSizeBytes, "Current WAL file size.", "collection", collection).Set(float64(bytes))
}

func (m *Metrics) SetFileSize(collection string, bytes int64) {
	m.reg.Gauge(MFileSizeBytes, "Total file size on disk.", "collection", collection).Set(float64(bytes))
}

func (m *Metrics) SetFragmentation(collection string, ratio float64) {
	m.reg.Gauge(MFragmentationRatio, "Free space over total file size.", "collection", collection).Set(ratio)
}

// candidateBuckets covers ANN search-cost counts; kBuckets covers requested k;
// fractionBuckets covers selectivity in [0,1].
var (
	candidateBuckets = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}
	kBuckets         = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000}
	fractionBuckets  = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 0.75, 0.9, 1.0}
)

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
