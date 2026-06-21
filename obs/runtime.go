package obs

import "runtime"

// GCBuckets covers GC stop-the-world pauses in seconds (spec 18 §2.8). The range
// runs from 50us to 100ms, which spans a healthy young-gen pause up to a pause
// long enough to show in the latency tail.
var GCBuckets = []float64{0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1}

// RuntimeCollector samples the Go runtime and folds the result into the runtime
// metrics of spec 18 §2.8: GC pause duration, heap allocated bytes, and live
// goroutine count. It is poll-based (spec §12.2): the server calls Collect on a
// ticker, default every 10 seconds. The collector keeps the last PauseTotalNs so
// each Collect records only the pauses since the previous call.
type RuntimeCollector struct {
	metrics      *Metrics
	lastPauseTot uint64
	lastNumGC    uint32
	started      bool
}

// NewRuntimeCollector builds a collector that records into m.
func NewRuntimeCollector(m *Metrics) *RuntimeCollector {
	return &RuntimeCollector{metrics: m}
}

// Collect reads runtime.MemStats once and updates the runtime metrics (spec 18
// §12.2). The first call seeds the GC baseline and records the gauges but emits no
// pause samples, because there is no previous reading to diff against. Later calls
// observe each GC pause that completed since the previous call, read from the
// PauseNs circular buffer.
func (c *RuntimeCollector) Collect() {
	if c.metrics == nil {
		return
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	c.metrics.reg.Gauge(MHeapAllocBytes, "Go heap allocated bytes.").Set(float64(ms.HeapAlloc))
	c.metrics.reg.Gauge(MGoroutines, "Live goroutines.").Set(float64(runtime.NumGoroutine()))

	if !c.started {
		c.started = true
		c.lastPauseTot = ms.PauseTotalNs
		c.lastNumGC = ms.NumGC
		return
	}

	// NumGC counts completed cycles; the number of new cycles since the last poll
	// is the gap, capped at the 256-entry PauseNs ring so a long gap does not read
	// stale slots.
	hist := c.metrics.reg.Histogram(MGCPauseDuration, "Go GC stop-the-world pause duration in seconds.", GCBuckets)
	newCycles := ms.NumGC - c.lastNumGC
	if newCycles > 256 {
		newCycles = 256
	}
	for i := uint32(0); i < newCycles; i++ {
		// PauseNs[(NumGC+255)%256] is the most recent pause; walk backward.
		idx := (ms.NumGC - 1 - i + 256) % 256
		hist.Observe(float64(ms.PauseNs[idx]) / 1e9)
	}
	c.lastPauseTot = ms.PauseTotalNs
	c.lastNumGC = ms.NumGC
}
