package bench

import "fmt"

// GateLevel is how a failed gate is surfaced (spec 20 §13.2). Blocking fails the
// CI job and blocks merge; Warning passes the job but annotates the PR.
type GateLevel int

const (
	Warning GateLevel = iota
	Blocking
)

// String renders the level for the alarm text.
func (l GateLevel) String() string {
	if l == Blocking {
		return "blocking"
	}
	return "warning"
}

// Direction is which way a metric is allowed to move (spec 20 §13.2). Higher is
// better for QPS, throughput, and recall; Lower is better for latency and build
// time.
type Direction int

const (
	HigherBetter Direction = iota
	LowerBetter
)

// Gate is one gated benchmark check (spec 20 §13.2). It compares a measured value
// against a baseline (the 90-day median) within a tolerance. The tolerance is a
// fraction: 0.05 means the metric may not move more than 5% in the bad direction.
type Gate struct {
	Name      string
	Metric    string
	Baseline  float64
	Tolerance float64 // fractional, e.g. 0.05 for 5%
	Direction Direction
	Level     GateLevel
}

// GateResult is the outcome of evaluating one gate.
type GateResult struct {
	Gate     Gate
	Measured float64
	Limit    float64 // the worst value still allowed
	Passed   bool
}

// Evaluate checks a measured value against the gate (spec 20 §13.2). For a
// HigherBetter metric the limit is baseline*(1-tolerance) and the value must be at
// or above it; for LowerBetter the limit is baseline*(1+tolerance) and the value
// must be at or below it.
func (g Gate) Evaluate(measured float64) GateResult {
	res := GateResult{Gate: g, Measured: measured}
	switch g.Direction {
	case HigherBetter:
		res.Limit = g.Baseline * (1 - g.Tolerance)
		res.Passed = measured >= res.Limit
	case LowerBetter:
		res.Limit = g.Baseline * (1 + g.Tolerance)
		res.Passed = measured <= res.Limit
	}
	return res
}

// Blocking reports whether this failed result blocks merge: it failed and its gate
// is Blocking. A passed result never blocks, and a failed Warning gate annotates
// but does not block.
func (r GateResult) Blocking() bool {
	return !r.Passed && r.Gate.Level == Blocking
}

// Alarm renders the regression alarm text (spec 20 §13.5). The caller posts it to
// the CI issue or the PR annotation. It names the metric, the before and after,
// the baseline, and the threshold, which is the format the runbook expects.
func (r GateResult) Alarm() string {
	pctMoved := 0.0
	if r.Gate.Baseline != 0 {
		pctMoved = (r.Measured - r.Gate.Baseline) / r.Gate.Baseline * 100
	}
	return fmt.Sprintf(
		"[PERF REGRESSION] %s %s moved %+.1f%% (baseline: %.4g, measured: %.4g)\n"+
			"Gate: %s | tolerance %.1f%% | limit %.4g | level %s",
		r.Gate.Name, r.Gate.Metric, pctMoved, r.Gate.Baseline, r.Measured,
		r.Gate.Name, r.Gate.Tolerance*100, r.Limit, r.Gate.Level)
}

// EvaluateGates evaluates a set of gates against measured values keyed by gate
// name (spec 20 §13.2). A gate with no measured value is skipped. The returned
// AnyBlocking reports whether the CI job should fail.
func EvaluateGates(gates []Gate, measured map[string]float64) (results []GateResult, anyBlocking bool) {
	for _, g := range gates {
		v, ok := measured[g.Name]
		if !ok {
			continue
		}
		r := g.Evaluate(v)
		results = append(results, r)
		if r.Blocking() {
			anyBlocking = true
		}
	}
	return results, anyBlocking
}
