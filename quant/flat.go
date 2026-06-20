package quant

import (
	"math"
	"unsafe"

	"github.com/tamnd/vector/distance"
)

// FlatQuantizer is the identity codec: no compression, fp32 stored verbatim
// (spec 09 §9.5). It implements Quantizer so the index code path is uniform; its
// NewADCTable returns an exact table rather than nil so a flat index can still go
// through the ADC interface, computing true distances. The planner selects flat
// for small collections where a quantized search plus rerank costs more than an
// exact scan (spec 09 §10.4).
type FlatQuantizer struct {
	D int
}

// NewFlatQuantizer returns a flat codec for dimension d.
func NewFlatQuantizer(d int) *FlatQuantizer { return &FlatQuantizer{D: d} }

func (q *FlatQuantizer) CodeSize() int        { return q.D * 4 }
func (q *FlatQuantizer) Dim() int             { return q.D }
func (q *FlatQuantizer) CodecKind() CodecKind { return CodecFlat }

// Encode copies the raw fp32 bytes into code.
func (q *FlatQuantizer) Encode(vec []float32, code []byte) {
	copy(code, unsafe.Slice((*byte)(unsafe.Pointer(&vec[0])), q.D*4))
}

// Decode reinterprets the raw bytes back to fp32.
func (q *FlatQuantizer) Decode(code []byte, out []float32) {
	copy(out, unsafe.Slice((*float32)(unsafe.Pointer(&code[0])), q.D))
}

// NewADCTable returns an exact table that decodes each code and computes the true
// metric distance to the query.
func (q *FlatQuantizer) NewADCTable(query []float32, metric distance.Metric) ADCTable {
	qq := make([]float32, q.D)
	copy(qq, query)
	return &flatADC{q: qq, D: q.D, metric: metric}
}

type flatADC struct {
	q      []float32
	D      int
	metric distance.Metric
}

func (t *flatADC) Distance(code []byte) float32 {
	x := unsafe.Slice((*float32)(unsafe.Pointer(&code[0])), t.D)
	switch t.metric {
	case distance.Dot:
		return -distance.DotFloat32(t.q, x)
	case distance.Cosine:
		return distance.CosineDistanceFloat32(t.q, x)
	case distance.L2:
		return float32(math.Sqrt(float64(distance.L2SquaredFloat32(t.q, x))))
	default:
		return distance.L2SquaredFloat32(t.q, x)
	}
}
