package quant

import (
	"math"

	"github.com/tamnd/vector/distance"
)

// BinaryQuantizer maps each dimension to a single bit by thresholding at zero (or
// at the per-dimension mean), giving D/8 bytes per vector with Hamming distance as
// the proxy metric (spec 09 §6). It works for high-dimensional near-unit-norm
// embeddings; the coarse Hamming proxy needs a larger rerank factor than PQ
// (spec 09 §6.2).
type BinaryQuantizer struct {
	D         int
	Threshold []float32 // length D; per-dimension threshold (zeros = threshold at 0)
}

// NewBinaryQuantizer returns a zero-threshold binary codec for dimension d.
func NewBinaryQuantizer(d int) *BinaryQuantizer {
	return &BinaryQuantizer{D: d, Threshold: make([]float32, d)}
}

// TrainBinary learns per-dimension mean thresholds from a sample (spec 09 §6.1),
// the better choice when dimensions are not centered at zero. data is n rows of d
// columns, row-major.
func TrainBinary(data []float32, n, d int) (*BinaryQuantizer, error) {
	if n == 0 {
		return nil, ErrEmptyTraining
	}
	if !isFinite(data[:n*d]) {
		return nil, ErrNonFinite
	}
	q := &BinaryQuantizer{D: d, Threshold: make([]float32, d)}
	acc := make([]float64, d)
	for i := 0; i < n; i++ {
		row := data[i*d : (i+1)*d]
		for j := 0; j < d; j++ {
			acc[j] += float64(row[j])
		}
	}
	for j := 0; j < d; j++ {
		q.Threshold[j] = float32(acc[j] / float64(n))
	}
	return q, nil
}

func (q *BinaryQuantizer) CodeSize() int        { return (q.D + 7) / 8 }
func (q *BinaryQuantizer) Dim() int             { return q.D }
func (q *BinaryQuantizer) CodecKind() CodecKind { return CodecBinary }

// Encode packs sign-vs-threshold bits MSB-first within each byte (spec 09 §5.4
// packing convention, reused for binary).
func (q *BinaryQuantizer) Encode(vec []float32, code []byte) {
	for i := range code[:q.CodeSize()] {
		code[i] = 0
	}
	for i := 0; i < q.D; i++ {
		if vec[i] >= q.Threshold[i] {
			code[i/8] |= 1 << (7 - uint(i%8))
		}
	}
}

// Decode reconstructs a coarse fp32 vector with +/-1 per dimension; rerank uses
// the raw store, so this is only a rough placeholder for callers that want a
// vector back.
func (q *BinaryQuantizer) Decode(code []byte, out []float32) {
	for i := 0; i < q.D; i++ {
		if code[i/8]&(1<<(7-uint(i%8))) != 0 {
			out[i] = 1
		} else {
			out[i] = -1
		}
	}
}

// NewADCTable returns nil: binary search uses Hamming distance on the codes
// directly, not an ADC table (spec 09 §9.5). The index computes Hamming via the
// distance kernel and reranks the top candidates.
func (q *BinaryQuantizer) NewADCTable(query []float32, metric distance.Metric) ADCTable {
	return nil
}

// RaBitQCodebook is the 1-bit RaBitQ codec (spec 09 §5). The code is just the
// sign bits of the (normalized) vector; an optional per-vector L2 norm is stored
// for L2-distance mode (spec 09 §5.4). RaBitQ carries a provable inner-product
// error bound that plain PQ lacks.
type RaBitQCodebook struct {
	D    int
	Norm bool // store per-vector L2 norm alongside the bit code (L2 mode)
}

// RaBitQQuantizer adapts RaBitQ to the Quantizer interface. The optional norm is
// appended as 4 trailing bytes when Norm is set, so CodeSize accounts for it.
type RaBitQQuantizer struct{ cb RaBitQCodebook }

// NewRaBitQQuantizer returns a RaBitQ codec for dimension d. withNorm appends a
// per-vector L2 norm for L2-distance collections (spec 09 §5.3).
func NewRaBitQQuantizer(d int, withNorm bool) *RaBitQQuantizer {
	return &RaBitQQuantizer{cb: RaBitQCodebook{D: d, Norm: withNorm}}
}

func (q *RaBitQQuantizer) CodeSize() int {
	n := (q.cb.D + 7) / 8
	if q.cb.Norm {
		n += 4
	}
	return n
}
func (q *RaBitQQuantizer) Dim() int             { return q.cb.D }
func (q *RaBitQQuantizer) CodecKind() CodecKind { return CodecRaBitQ }

// Encode packs sign bits MSB-first and, in norm mode, appends the L2 norm as a
// little-endian float32 (spec 09 §5.4).
func (q *RaBitQQuantizer) Encode(vec []float32, code []byte) {
	bitLen := (q.cb.D + 7) / 8
	for i := 0; i < bitLen; i++ {
		code[i] = 0
	}
	for i := 0; i < q.cb.D; i++ {
		if vec[i] >= 0 {
			code[i/8] |= 1 << (7 - uint(i%8))
		}
	}
	if q.cb.Norm {
		var n float64
		for _, v := range vec[:q.cb.D] {
			n += float64(v) * float64(v)
		}
		putFloat32(code[bitLen:], float32(math.Sqrt(n)))
	}
}

// Decode reconstructs a +/-1 sign vector scaled by 1/sqrt(D), the RaBitQ
// reconstruction (spec 09 §5.1). In norm mode it rescales by the stored norm.
func (q *RaBitQQuantizer) Decode(code []byte, out []float32) {
	bitLen := (q.cb.D + 7) / 8
	scale := float32(1.0 / math.Sqrt(float64(q.cb.D)))
	if q.cb.Norm {
		scale = getFloat32(code[bitLen:]) / float32(math.Sqrt(float64(q.cb.D)))
	}
	for i := 0; i < q.cb.D; i++ {
		if code[i/8]&(1<<(7-uint(i%8))) != 0 {
			out[i] = scale
		} else {
			out[i] = -scale
		}
	}
}

// NewADCTable returns nil: RaBitQ uses the asymmetric popcount-and-hadamard path
// on the codes, not an ADC table (spec 09 §5.2, §9.5).
func (q *RaBitQQuantizer) NewADCTable(query []float32, metric distance.Metric) ADCTable {
	return nil
}
