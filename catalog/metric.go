package catalog

import "github.com/tamnd/vector/distance"

// ElementType is the stored element representation of a vector column (spec 02 §4.3).
type ElementType uint8

const (
	ElemFP32   ElementType = 1 // 4 bytes/elem, default (spec 02 §4.3)
	ElemFP16   ElementType = 2 // 2 bytes/elem
	ElemInt8   ElementType = 3 // 1 byte/elem, scalar-quantized, dequantized on read
	ElemBinary ElementType = 4 // 1 bit/elem packed, Hamming/Jaccard only
)

// String renders an ElementType as its SQL keyword (spec 02 §4.3).
func (e ElementType) String() string {
	switch e {
	case ElemFP32:
		return "fp32"
	case ElemFP16:
		return "fp16"
	case ElemInt8:
		return "int8"
	case ElemBinary:
		return "binary"
	default:
		return "elem?"
	}
}

// Metric is the distance metric bound to a vector column (spec 02 §8). The metric
// is a property of the data, fixed once any index is built (spec 02 §8.1, §8.6).
type Metric uint8

const (
	MetricCosine       Metric = 1 // <=> vector_cosine_ops (spec 02 §8.2)
	MetricL2           Metric = 2 // <-> vector_l2_ops
	MetricInnerProduct Metric = 3 // <#> vector_ip_ops
	MetricHamming      Metric = 4 // <~> vector_hamming_ops, binary only
	MetricJaccard      Metric = 5 // <%> vector_jaccard_ops, binary only
	MetricDotSparse    Metric = 6 // <#> sparsevec_ip_ops, sparse only
)

// String renders a Metric for diagnostics and the catalog (spec 02 §8.2).
func (m Metric) String() string {
	switch m {
	case MetricCosine:
		return "cosine"
	case MetricL2:
		return "l2"
	case MetricInnerProduct:
		return "inner_product"
	case MetricHamming:
		return "hamming"
	case MetricJaccard:
		return "jaccard"
	case MetricDotSparse:
		return "dot_sparse"
	default:
		return "metric?"
	}
}

// Opclass renders the index operator class name bound to this metric (spec 02
// §8.2, §15 glossary): the pgvector-compatible opclass identifier.
func (m Metric) Opclass() string {
	switch m {
	case MetricCosine:
		return "vector_cosine_ops"
	case MetricL2:
		return "vector_l2_ops"
	case MetricInnerProduct:
		return "vector_ip_ops"
	case MetricHamming:
		return "vector_hamming_ops"
	case MetricJaccard:
		return "vector_jaccard_ops"
	case MetricDotSparse:
		return "sparsevec_ip_ops"
	default:
		return ""
	}
}

// DefaultMetric returns the metric applied to a vector column of the given
// element type when no METRIC clause is given (spec 02 §8.7).
func DefaultMetric(e ElementType) Metric {
	switch e {
	case ElemFP32, ElemFP16:
		return MetricCosine
	case ElemInt8:
		return MetricL2
	case ElemBinary:
		return MetricHamming
	default:
		return MetricCosine
	}
}

// MetricSupported reports whether a metric is valid for an element type
// (spec 02 §8.2 normative table, §13.10). Binary vectors take only Hamming and
// Jaccard; float and int8 vectors take L2, cosine, and inner product.
func MetricSupported(m Metric, e ElementType) bool {
	switch m {
	case MetricCosine, MetricL2, MetricInnerProduct:
		return e == ElemFP32 || e == ElemFP16 || e == ElemInt8
	case MetricHamming, MetricJaccard:
		return e == ElemBinary
	case MetricDotSparse:
		return false // sparse columns are a distinct column kind, not handled here
	default:
		return false
	}
}

// distanceMetric lowers a catalog Metric to the engine's distance.Metric used by
// the kernels (spec 02 §11, spec 09 §12.1). Cosine and inner product map to
// their kernels directly; L2 lowers to L2Squared, the index working metric
// (spec 02 §8.3, the index ranks by squared L2 and the result label is L2).
func (m Metric) distanceMetric() distance.Metric {
	switch m {
	case MetricCosine:
		return distance.Cosine
	case MetricL2:
		return distance.L2Squared
	case MetricInnerProduct:
		return distance.Dot
	case MetricHamming:
		return distance.Hamming
	case MetricJaccard:
		return distance.Jaccard
	default:
		return distance.L2Squared
	}
}
