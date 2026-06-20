// Package quant implements vec's vector codecs: flat (no compression), scalar
// quantization (SQ int8), product quantization (PQ), optimized product
// quantization (OPQ), binary (1-bit Hamming), and RaBitQ (spec 09 §2-8). Every
// codec is reached through the Quantizer interface so the HNSW, IVF, and DiskANN
// indexes hold a Quantizer and never branch on the concrete codec.
//
// The quantize-and-rerank principle (spec 09 §1.2) is the reason these exist: a
// lossy codec shrinks the working set so the ANN traversal stays in cache, then a
// rerank pass over the raw vectors (spec 09 §10) recovers the recall the codec
// lost. This package owns the codecs and their training; the rerank pass and the
// raw-vector store live in the storage and query layers.
package quant

import (
	"errors"

	"github.com/tamnd/vector/distance"
)

// Errors returned by training and codebook decoding.
var (
	// ErrNotDivisible is returned when a PQ/OPQ configuration has a dimension not
	// divisible by the subspace count (spec 09 §3.2).
	ErrNotDivisible = errors.New("quant: vector dimension not divisible by subspace count")
	// ErrNonFinite is returned when a training vector contains NaN or Inf; encoding
	// a non-finite value is undefined, so training rejects it (spec 09 §13.2).
	ErrNonFinite = errors.New("quant: training vector contains NaN or Inf")
	// ErrEmptyTraining is returned when training is asked to run on no vectors.
	ErrEmptyTraining = errors.New("quant: empty training set")
	// ErrBadCodebook is returned when a codebook page fails magic or shape checks.
	ErrBadCodebook = errors.New("quant: corrupt or unrecognized codebook")
	// ErrCodeSize is returned when an Encode/Decode buffer is the wrong length.
	ErrCodeSize = errors.New("quant: code buffer has the wrong size")
)

// Quantizer abstracts a codec for the ANN indexes (spec 09 §9.5). An index holds
// one Quantizer and routes every distance evaluation either through an ADCTable
// (PQ/OPQ) or through Decode plus an exact kernel (flat/SQ/binary/RaBitQ).
type Quantizer interface {
	// CodeSize returns the number of bytes one encoded vector occupies.
	CodeSize() int
	// Dim returns the original (uncompressed) vector dimension.
	Dim() int
	// Encode writes the code for vec into code, which must be CodeSize() bytes.
	Encode(vec []float32, code []byte)
	// Decode reconstructs an approximate fp32 vector from code into out, which must
	// be Dim() elements.
	Decode(code []byte, out []float32)
	// NewADCTable builds a query-to-centroid distance table for ADC search, or
	// returns nil if the codec does not support ADC (flat, binary, RaBitQ, SQ): the
	// caller then falls back to Decode plus an exact kernel (spec 09 §9.5).
	NewADCTable(query []float32, metric distance.Metric) ADCTable
	// CodecKind identifies the concrete codec, for the codebook page and diagnostics.
	CodecKind() CodecKind
}

// ADCTable is a per-query lookup table giving approximate distances to coded
// vectors without dequantizing them (spec 09 §9.1).
type ADCTable interface {
	// Distance returns the approximate distance from the query this table was built
	// for to the point with the given code.
	Distance(code []byte) float32
}

// CodecKind names a codec for the on-disk codebook and the planner.
type CodecKind uint8

const (
	CodecFlat CodecKind = iota
	CodecSQ
	CodecPQ
	CodecOPQ
	CodecBinary
	CodecRaBitQ
)

// String renders a CodecKind.
func (k CodecKind) String() string {
	switch k {
	case CodecFlat:
		return "flat"
	case CodecSQ:
		return "sq"
	case CodecPQ:
		return "pq"
	case CodecOPQ:
		return "opq"
	case CodecBinary:
		return "binary"
	case CodecRaBitQ:
		return "rabitq"
	default:
		return "codec?"
	}
}

// isFinite reports whether every element of v is finite (no NaN, no Inf).
func isFinite(v []float32) bool {
	for _, x := range v {
		// x != x is true only for NaN; the Inf check uses the fact that +/-Inf
		// exceeds every finite magnitude.
		if x != x || x > 3.4e38 || x < -3.4e38 {
			return false
		}
	}
	return true
}
