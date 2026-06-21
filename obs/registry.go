package obs

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// metricType is the Prometheus exposition type line.
type metricType string

const (
	typeCounter   metricType = "counter"
	typeGauge     metricType = "gauge"
	typeHistogram metricType = "histogram"
)

// family is one named metric with a set of label series (spec 18 section 2.10).
// A family owns its series map under a mutex; the series values themselves update
// lock-free, so the mutex is taken only to find or create a series, not on every
// hot-path observation of an existing series.
type family struct {
	name string
	help string
	typ  metricType

	mu        sync.RWMutex
	counters  map[string]*Counter
	gauges    map[string]*Gauge
	hists     map[string]*Histogram
	histBound []float64
}

// Registry holds every metric family and renders Prometheus text exposition
// (spec 18 section 1.5, 2.10). It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	families map[string]*family
	order    []string // family registration order, for stable output
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{families: make(map[string]*family)}
}

func (r *Registry) family(name, help string, typ metricType, bounds []float64) *family {
	r.mu.RLock()
	f := r.families[name]
	r.mu.RUnlock()
	if f != nil {
		return f
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f = r.families[name]; f != nil {
		return f
	}
	f = &family{
		name:      name,
		help:      help,
		typ:       typ,
		counters:  make(map[string]*Counter),
		gauges:    make(map[string]*Gauge),
		hists:     make(map[string]*Histogram),
		histBound: bounds,
	}
	r.families[name] = f
	r.order = append(r.order, name)
	return f
}

// Counter returns the counter for a metric name and label pairs, creating it on
// first use (spec 18 section 2.2). Help is recorded the first time the name is
// seen.
func (r *Registry) Counter(name, help string, labelPairs ...string) *Counter {
	f := r.family(name, help, typeCounter, nil)
	key := labels(labelPairs...).key()
	f.mu.RLock()
	c := f.counters[key]
	f.mu.RUnlock()
	if c != nil {
		return c
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c = f.counters[key]; c == nil {
		c = &Counter{}
		f.counters[key] = c
	}
	return c
}

// Gauge returns the gauge for a metric name and label pairs (spec 18 section 2.5).
func (r *Registry) Gauge(name, help string, labelPairs ...string) *Gauge {
	f := r.family(name, help, typeGauge, nil)
	key := labels(labelPairs...).key()
	f.mu.RLock()
	g := f.gauges[key]
	f.mu.RUnlock()
	if g != nil {
		return g
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if g = f.gauges[key]; g == nil {
		g = &Gauge{}
		f.gauges[key] = g
	}
	return g
}

// Histogram returns the histogram for a metric name and label pairs (spec 18
// section 2.2). The bounds are used only when the series is first created.
func (r *Registry) Histogram(name, help string, bounds []float64, labelPairs ...string) *Histogram {
	f := r.family(name, help, typeHistogram, bounds)
	key := labels(labelPairs...).key()
	f.mu.RLock()
	h := f.hists[key]
	f.mu.RUnlock()
	if h != nil {
		return h
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if h = f.hists[key]; h == nil {
		h = NewHistogram(bounds)
		f.hists[key] = h
	}
	return h
}

// WriteText renders the whole registry in Prometheus text exposition format
// (spec 18 section 1.5). Families come out in registration order and series
// within a family come out in sorted label order, so the output is deterministic.
func (r *Registry) WriteText(w *strings.Builder) {
	r.mu.RLock()
	order := append([]string(nil), r.order...)
	fams := make(map[string]*family, len(r.families))
	for k, v := range r.families {
		fams[k] = v
	}
	r.mu.RUnlock()

	for _, name := range order {
		fams[name].writeText(w)
	}
}

// Text returns the exposition as a string.
func (r *Registry) Text() string {
	var b strings.Builder
	r.WriteText(&b)
	return b.String()
}

func (f *family) writeText(w *strings.Builder) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.help != "" {
		w.WriteString("# HELP ")
		w.WriteString(f.name)
		w.WriteByte(' ')
		w.WriteString(f.help)
		w.WriteByte('\n')
	}
	w.WriteString("# TYPE ")
	w.WriteString(f.name)
	w.WriteByte(' ')
	w.WriteString(string(f.typ))
	w.WriteByte('\n')

	switch f.typ {
	case typeCounter:
		for _, key := range sortedKeys(f.counters) {
			writeSample(w, f.name, key, f.counters[key].Value())
		}
	case typeGauge:
		for _, key := range sortedKeysG(f.gauges) {
			writeSampleF(w, f.name, key, f.gauges[key].Value())
		}
	case typeHistogram:
		for _, key := range sortedKeysH(f.hists) {
			f.writeHistogram(w, key, f.hists[key])
		}
	}
}

func (f *family) writeHistogram(w *strings.Builder, key string, h *Histogram) {
	cum := h.cumulative()
	base := splitBrace(key)
	for i, ub := range h.bounds {
		w.WriteString(f.name)
		w.WriteString("_bucket")
		w.WriteString(withLabel(base, "le", formatFloat(ub)))
		w.WriteByte(' ')
		w.WriteString(strconv.FormatInt(cum[i], 10))
		w.WriteByte('\n')
	}
	w.WriteString(f.name)
	w.WriteString("_bucket")
	w.WriteString(withLabel(base, "le", "+Inf"))
	w.WriteByte(' ')
	w.WriteString(strconv.FormatInt(cum[len(cum)-1], 10))
	w.WriteByte('\n')

	w.WriteString(f.name)
	w.WriteString("_sum")
	w.WriteString(key)
	w.WriteByte(' ')
	w.WriteString(formatFloat(h.Sum()))
	w.WriteByte('\n')

	w.WriteString(f.name)
	w.WriteString("_count")
	w.WriteString(key)
	w.WriteByte(' ')
	w.WriteString(strconv.FormatInt(h.Count(), 10))
	w.WriteByte('\n')
}

func writeSample(w *strings.Builder, name, key string, v int64) {
	w.WriteString(name)
	w.WriteString(key)
	w.WriteByte(' ')
	w.WriteString(strconv.FormatInt(v, 10))
	w.WriteByte('\n')
}

func writeSampleF(w *strings.Builder, name, key string, v float64) {
	w.WriteString(name)
	w.WriteString(key)
	w.WriteByte(' ')
	w.WriteString(formatFloat(v))
	w.WriteByte('\n')
}

// splitBrace returns the label pairs inside a {..} key, without the braces, or ""
// for an unlabeled series.
func splitBrace(key string) string {
	if len(key) < 2 {
		return ""
	}
	return key[1 : len(key)-1]
}

// withLabel renders a label block that adds one more name=value to an existing
// inner label string (used to append the histogram le label).
func withLabel(inner, name, value string) string {
	var b strings.Builder
	b.WriteByte('{')
	if inner != "" {
		b.WriteString(inner)
		b.WriteByte(',')
	}
	b.WriteString(name)
	b.WriteString(`="`)
	b.WriteString(value)
	b.WriteString(`"}`)
	return b.String()
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func sortedKeys(m map[string]*Counter) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysG(m map[string]*Gauge) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysH(m map[string]*Histogram) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
