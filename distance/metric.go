// Package distance implements vec's scalar distance kernels and the kernel
// dispatch architecture (spec 09 §11-13). Every distance computation in vec goes
// through this package: the flat scan, the HNSW and IVF traversals, PQ
// asymmetric distance, and rerank all call a kernel selected here by (metric,
// element type).
//
// The pure-Go kernels in this package are the specification and the correctness
// oracle (spec 09 §11.3): a SIMD variant is correct iff it matches the pure-Go
// result within the fp32 associativity tolerance. SIMD tiers (AVX-512, AVX2,
// NEON) register higher-throughput kernels for their (metric, element type)
// combinations; any combination a tier does not supply falls back to the scalar
// kernel, which is always complete (spec 09 §12.1). The Go-assembly tiers are a
// later slice; this build ships the always-correct scalar tier and the dispatch
// seam they slot into.
//
// The spec places these kernels under internal/kernels; vec does not use Go
// internal/ package trees, so they live in this importable package instead.
package distance

// Metric identifies the distance function a collection's vector column uses
// (spec 09 §1.4, §12.1).
type Metric uint8

const (
	L2Squared Metric = iota // squared Euclidean, the HNSW/IVF working metric
	L2                      // Euclidean (sqrt of L2Squared)
	Cosine                  // 1 - cosine similarity
	Dot                     // negative inner product (larger similarity = smaller distance)
	Hamming                 // differing-bit count over binary codes
	Jaccard                 // 1 - |A&B|/|A|B| over binary codes
)

// String renders a Metric for diagnostics and the planner.
func (m Metric) String() string {
	switch m {
	case L2Squared:
		return "l2sq"
	case L2:
		return "l2"
	case Cosine:
		return "cosine"
	case Dot:
		return "dot"
	case Hamming:
		return "hamming"
	case Jaccard:
		return "jaccard"
	default:
		return "metric?"
	}
}

// ElemType identifies the in-memory element encoding a kernel consumes
// (spec 09 §12.1). It is the codec's element type, distinct from the metric: the
// quantizer knows the element type, the column knows the metric, and together
// they select a kernel.
type ElemType uint8

const (
	Float32 ElemType = iota // raw fp32, 4 bytes/dim
	Float16                 // IEEE 754 half, 2 bytes/dim (stored as uint16)
	Int8                    // scalar-quantized signed 8-bit
	Bit                     // 1-bit binary code, 1 bit/dim packed into bytes
)

// String renders an ElemType for diagnostics.
func (e ElemType) String() string {
	switch e {
	case Float32:
		return "f32"
	case Float16:
		return "f16"
	case Int8:
		return "i8"
	case Bit:
		return "bit"
	default:
		return "elem?"
	}
}

// MetricKernelKey selects a kernel from the registry (spec 09 §12.1).
type MetricKernelKey struct {
	Metric   Metric
	ElemType ElemType
}

// DistanceFn is the unified distance signature (spec 09 §12.1). a and b point to
// raw element-typed memory passed as bytes for genericity; D is the vector
// dimension (the logical element count, not the byte length). The kernel
// reinterprets the bytes according to its element type.
type DistanceFn func(a, b []byte, D int) float32

// SIMDTier names the dispatch tier selected at init (spec 09 §12.3). Only
// TierScalar is wired in this build; the asm tiers register when their kernels
// land, and anything they omit falls back to scalar.
type SIMDTier uint8

const (
	TierScalar SIMDTier = iota
	TierNEON
	TierAVX2FMA
	TierAVX512
	TierAVX512VNNI
)

// String renders a SIMDTier for diagnostics and the PRAGMA that reports it.
func (t SIMDTier) String() string {
	switch t {
	case TierScalar:
		return "scalar"
	case TierNEON:
		return "neon"
	case TierAVX2FMA:
		return "avx2+fma"
	case TierAVX512:
		return "avx512"
	case TierAVX512VNNI:
		return "avx512+vnni"
	default:
		return "tier?"
	}
}
