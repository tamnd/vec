package server

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Metrics is the server's Prometheus exposition (spec 16 §8.2). It is a small
// registry of counters and gauges plus the Go runtime stats, rendered as the
// Prometheus text format. It implements http.Handler so it can be mounted at
// /metrics on the REST listener or a separate metrics listener.
type Metrics struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
	help     map[string]string
}

// newMetrics builds an empty registry.
func newMetrics() *Metrics {
	return &Metrics{
		counters: make(map[string]float64),
		gauges:   make(map[string]float64),
		help:     make(map[string]string),
	}
}

// metricKey formats a metric name and its sorted labels into a series key.
func metricKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", k, labels[k])
	}
	b.WriteByte('}')
	return b.String()
}

// incCounter adds delta to a counter series.
func (m *Metrics) incCounter(name string, labels map[string]string, delta float64) {
	m.mu.Lock()
	m.counters[metricKey(name, labels)] += delta
	m.mu.Unlock()
}

// setGauge sets a gauge series.
func (m *Metrics) setGauge(name string, labels map[string]string, v float64) {
	m.mu.Lock()
	m.gauges[metricKey(name, labels)] = v
	m.mu.Unlock()
}

// addGauge adds delta to a gauge series, for tracking active counts.
func (m *Metrics) addGauge(name string, labels map[string]string, delta float64) {
	m.mu.Lock()
	m.gauges[metricKey(name, labels)] += delta
	m.mu.Unlock()
}

// observeRequest records one served request and its outcome.
func (m *Metrics) observeRequest(method, collection, status string) {
	m.incCounter("vec_requests_total", map[string]string{
		"method": method, "collection": collection, "status": status,
	}, 1)
}

// ServeHTTP renders the registry in the Prometheus text exposition format.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	counters := snapshot(m.counters)
	gaugeMap := make(map[string]float64, len(m.gauges))
	for k, v := range m.gauges {
		gaugeMap[k] = v
	}
	m.mu.Unlock()

	var rt runtime.MemStats
	runtime.ReadMemStats(&rt)
	gaugeMap["vec_go_goroutines"] = float64(runtime.NumGoroutine())
	gaugeMap["vec_memory_alloc_bytes"] = float64(rt.Alloc)
	gauges := snapshot(gaugeMap)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	for _, line := range counters {
		_, _ = fmt.Fprintf(w, "%s %g\n", line.key, line.val)
	}
	for _, line := range gauges {
		_, _ = fmt.Fprintf(w, "%s %g\n", line.key, line.val)
	}
}

type kv struct {
	key string
	val float64
}

// snapshot copies a metric map into a sorted slice for stable output.
func snapshot(in map[string]float64) []kv {
	out := make([]kv, 0, len(in))
	for k, v := range in {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}
