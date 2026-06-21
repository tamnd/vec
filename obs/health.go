package obs

// Health status values (spec 18 §7.1). ok passes traffic; degraded passes traffic
// but fires an alert; not_ready blocks traffic.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
	StatusNotReady = "not_ready"
)

// HealthReport is the body of the /health and /ready endpoints (spec 18 §7.1).
// The top-level status is the worst status across collections: not_ready if any
// collection is not ready, else degraded if any is degraded, else ok.
type HealthReport struct {
	Status      string                      `json:"status"`
	Collections map[string]CollectionHealth `json:"collections"`
	UptimeS     int64                       `json:"uptime_s"`
	Version     string                      `json:"version"`
}

// CollectionHealth is the per-collection health detail (spec 18 §7.1).
type CollectionHealth struct {
	Status             string  `json:"status"`
	IndexLoaded        bool    `json:"index_loaded"`
	WALReplayed        bool    `json:"wal_replayed"`
	RecallEstimate     float64 `json:"recall_estimate"`
	RecallEstimateAgeS float64 `json:"recall_estimate_age_s"`
	// Reason names the degraded or not_ready condition, empty when ok. It is not
	// in the spec's JSON example but the endpoint includes it so an operator sees
	// why without cross-referencing the metrics.
	Reason string `json:"reason,omitempty"`
}

// HealthThresholds are the limits that turn an ok collection degraded (spec 18
// §7.1, §17.5). Zero on a field disables that check.
type HealthThresholds struct {
	// RecallWarnAgeS marks a collection degraded when its recall estimate is older
	// than this many seconds (default 3600, spec §17.5 health_recall_warn_age_s).
	RecallWarnAgeS float64
	// RecallFloor marks a collection degraded when its recall estimate is below
	// this floor. Zero disables the floor check.
	RecallFloor float64
	// WALSizeWarnBytes marks a collection degraded when its WAL exceeds this size
	// (default 128MB, spec §17.5 health_wal_size_warn_bytes).
	WALSizeWarnBytes int64
	// FragmentationWarn marks a collection degraded above this ratio (default 0.50,
	// spec §17.5 health_fragmentation_warn).
	FragmentationWarn float64
}

// DefaultHealthThresholds returns the spec §17.5 defaults.
func DefaultHealthThresholds() HealthThresholds {
	return HealthThresholds{
		RecallWarnAgeS:    3600,
		WALSizeWarnBytes:  128 << 20,
		FragmentationWarn: 0.50,
	}
}

// CollectionSnapshot is the raw state of one collection that the health evaluator
// grades against the thresholds. The server fills it from the engine; the obs
// package does not reach into storage.
type CollectionSnapshot struct {
	IndexLoaded        bool
	WALReplayed        bool
	RecallEstimate     float64
	RecallEstimateAgeS float64
	WALSizeBytes       int64
	Fragmentation      float64
}

// Grade turns one snapshot into a CollectionHealth (spec 18 §7.1). A collection
// that has not loaded its index or replayed its WAL is not_ready and blocks
// traffic. A loaded, replayed collection is degraded when any warn threshold is
// crossed, else ok. The first crossed condition is named in Reason.
func (t HealthThresholds) Grade(s CollectionSnapshot) CollectionHealth {
	h := CollectionHealth{
		IndexLoaded:        s.IndexLoaded,
		WALReplayed:        s.WALReplayed,
		RecallEstimate:     s.RecallEstimate,
		RecallEstimateAgeS: s.RecallEstimateAgeS,
	}
	if !s.WALReplayed {
		h.Status = StatusNotReady
		h.Reason = "wal not replayed"
		return h
	}
	if !s.IndexLoaded {
		h.Status = StatusNotReady
		h.Reason = "index not loaded"
		return h
	}
	switch {
	case t.RecallFloor > 0 && s.RecallEstimate > 0 && s.RecallEstimate < t.RecallFloor:
		h.Status = StatusDegraded
		h.Reason = "recall below floor"
	case t.RecallWarnAgeS > 0 && s.RecallEstimateAgeS > t.RecallWarnAgeS:
		h.Status = StatusDegraded
		h.Reason = "recall estimate stale"
	case t.WALSizeWarnBytes > 0 && s.WALSizeBytes > t.WALSizeWarnBytes:
		h.Status = StatusDegraded
		h.Reason = "wal growing without checkpoint"
	case t.FragmentationWarn > 0 && s.Fragmentation > t.FragmentationWarn:
		h.Status = StatusDegraded
		h.Reason = "fragmentation high"
	default:
		h.Status = StatusOK
	}
	return h
}

// BuildHealthReport grades every collection snapshot and rolls the per-collection
// statuses up to a single top-level status (spec 18 §7.1). version and uptimeS are
// stamped by the caller, which holds the clock.
func BuildHealthReport(t HealthThresholds, snaps map[string]CollectionSnapshot, version string, uptimeS int64) HealthReport {
	rep := HealthReport{
		Status:      StatusOK,
		Collections: make(map[string]CollectionHealth, len(snaps)),
		UptimeS:     uptimeS,
		Version:     version,
	}
	for name, s := range snaps {
		h := t.Grade(s)
		rep.Collections[name] = h
		rep.Status = worseStatus(rep.Status, h.Status)
	}
	return rep
}

// worseStatus returns the more severe of two statuses, ordered ok < degraded <
// not_ready.
func worseStatus(a, b string) string {
	if statusRank(b) > statusRank(a) {
		return b
	}
	return a
}

func statusRank(s string) int {
	switch s {
	case StatusNotReady:
		return 2
	case StatusDegraded:
		return 1
	default:
		return 0
	}
}

// Ready reports whether the report permits traffic (spec 18 §7.1). not_ready
// blocks; ok and degraded pass.
func (r HealthReport) Ready() bool {
	return r.Status != StatusNotReady
}
