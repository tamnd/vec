package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// Result is the benchmark result document (spec 20 §7.6): the run metadata, the
// build measurement, and the per-effort sweep. It serializes to the JSON schema
// the spec fixes and to a companion TSV for plotting.
type Result struct {
	Meta  Meta         `json:"meta"`
	Build Build        `json:"build"`
	Sweep []SweepPoint `json:"sweep"`
}

// Meta is the reproducibility block (spec 20 §4.4, §7.6). Every field that makes a
// result comparable across machines lives here, so the JSON is self-describing.
type Meta struct {
	VecVersion    string         `json:"vec_version"`
	GitSHA        string         `json:"git_sha"`
	Dataset       string         `json:"dataset"`
	DatasetSHA256 string         `json:"dataset_sha256"`
	Index         string         `json:"index"`
	BuildParams   map[string]int `json:"build_params"`
	K             int            `json:"k"`
	NQueries      int            `json:"n_queries"`
	NPoints       int            `json:"n_points"`
	Dimension     int            `json:"dimension"`
	Distance      string         `json:"distance"`
	Concurrency   int            `json:"concurrency"`
	Hardware      Hardware       `json:"hardware"`
	GoVersion     string         `json:"go_version"`
	OS            string         `json:"os"`
}

// Hardware documents the machine (spec 20 §4.4).
type Hardware struct {
	CPU     string `json:"cpu"`
	Cores   int    `json:"cores"`
	RAMGB   int    `json:"ram_gb"`
	Storage string `json:"storage"`
}

// Build is the index-build measurement (spec 20 §2.4, §2.5).
type Build struct {
	DurationSec    float64 `json:"duration_sec"`
	PeakRSSMB      int64   `json:"peak_rss_mb"`
	IndexSizeBytes int64   `json:"index_size_bytes"`
}

// WriteJSON writes the result as indented JSON (spec 20 §7.6).
func (r *Result) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteTSV writes the sweep as a tab-separated table with a header row (spec 20
// §7.6 companion .tsv). One row per sweep point, columns in the SweepPoint field
// order, so the file plots without post-processing.
func (r *Result) WriteTSV(w io.Writer) error {
	header := "param\tvalue\trecall10\tqps\tp50us\tp95us\tp99us\tp999us\tp9999us\tmax_us\tcpu_pct\tgc_pause_us\n"
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	for _, p := range r.Sweep {
		row := p.Param + "\t" +
			strconv.Itoa(p.Value) + "\t" +
			strconv.FormatFloat(p.Recall10, 'f', 4, 64) + "\t" +
			strconv.FormatFloat(p.QPS, 'f', 1, 64) + "\t" +
			strconv.FormatInt(p.P50us, 10) + "\t" +
			strconv.FormatInt(p.P95us, 10) + "\t" +
			strconv.FormatInt(p.P99us, 10) + "\t" +
			strconv.FormatInt(p.P999us, 10) + "\t" +
			strconv.FormatInt(p.P9999us, 10) + "\t" +
			strconv.FormatInt(p.MaxUs, 10) + "\t" +
			strconv.FormatFloat(p.CPUPct, 'f', 1, 64) + "\t" +
			strconv.FormatInt(p.GCPauseUs, 10) + "\n"
		if _, err := io.WriteString(w, row); err != nil {
			return err
		}
	}
	return nil
}

// QPSAtRecall returns the highest QPS among sweep points whose recall is at least
// floor (spec 20 §2.2: QPS reported at a fixed recall level). It returns 0 and
// false when no point reaches the floor. This is the number the recall-vs-QPS
// operating point and the CI gate read.
func (r *Result) QPSAtRecall(floor float64) (float64, bool) {
	best := 0.0
	found := false
	for _, p := range r.Sweep {
		if p.Recall10 >= floor && p.QPS > best {
			best = p.QPS
			found = true
		}
	}
	return best, found
}

// ParetoTable renders the recall/QPS sweep as an aligned text table (spec 20
// §12.1). It is the human-readable companion to the JSON the harness prints to the
// terminal.
func (r *Result) ParetoTable() string {
	out := fmt.Sprintf("%-10s %-10s %-12s %8s %8s %8s\n", "param", "value", "recall@10", "qps", "p50us", "p99us")
	for _, p := range r.Sweep {
		out += fmt.Sprintf("%-10s %-10d %-12.4f %8.0f %8d %8d\n",
			p.Param, p.Value, p.Recall10, p.QPS, p.P50us, p.P99us)
	}
	return out
}
