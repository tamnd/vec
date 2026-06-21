package bulk

import (
	"strings"

	vec "github.com/tamnd/vec"
)

// The dump carries the per-column metric on the CREATE INDEX opclass, the same way
// pgvector does, because the VectorSQL CREATE TABLE type does not carry a metric.
// These two functions are the single source of the opclass-to-metric mapping that
// dump (metric to opclass) and load (opclass to metric) share.

// vectorMetric returns the metric of a collection's vector column, defaulting to
// L2 when the collection somehow has no vector column.
func vectorMetric(info vec.CollectionInfo) vec.Metric {
	for _, c := range info.Columns {
		if c.Type == vec.TypeVector {
			return c.Metric
		}
	}
	return vec.MetricL2
}

func opclassForMetric(m vec.Metric) string {
	switch m {
	case vec.MetricCosine:
		return "vector_cosine_ops"
	case vec.MetricDot:
		return "vector_ip_ops"
	case vec.MetricHamming:
		return "bit_hamming_ops"
	case vec.MetricJaccard:
		return "bit_jaccard_ops"
	default:
		return "vector_l2_ops"
	}
}

// metricFromOpclass maps a pgvector opclass back to a metric. An unknown opclass
// defaults to L2, matching the load contract in spec 17 §3.4.
func metricFromOpclass(opclass string) vec.Metric {
	switch strings.ToLower(strings.TrimSpace(opclass)) {
	case "vector_cosine_ops":
		return vec.MetricCosine
	case "vector_ip_ops", "vector_dot_ops", "vector_inner_product_ops":
		return vec.MetricDot
	case "bit_hamming_ops", "hamming_ops":
		return vec.MetricHamming
	case "bit_jaccard_ops", "jaccard_ops":
		return vec.MetricJaccard
	default:
		return vec.MetricL2
	}
}
