package quant

import "github.com/tamnd/vector/distance"

// SQCodebook holds the per-dimension ranges learned for scalar quantization
// (spec 09 §2.4). vec uses per-dimension asymmetric SQ by default: each dimension
// gets its own min and max, so anisotropic embeddings (most transformer outputs)
// quantize without an outlier dimension dominating the range (spec 09 §2.2).
type SQCodebook struct {
	D    int       // vector dimension
	Min  []float32 // length D, per-dimension minimum
	Max  []float32 // length D, per-dimension maximum
	Bits int       // 8 (int8); 4-bit nibble is a future extension
}

// TrainSQ learns per-dimension ranges from a training sample (spec 09 §2.3). data
// is n rows of d columns, row-major. It is a single pass: the min and max of each
// dimension over the sample. Non-finite training vectors are rejected
// (spec 09 §13.2).
func TrainSQ(data []float32, n, d int) (*SQCodebook, error) {
	if n == 0 {
		return nil, ErrEmptyTraining
	}
	if !isFinite(data[:n*d]) {
		return nil, ErrNonFinite
	}
	cb := &SQCodebook{D: d, Bits: 8, Min: make([]float32, d), Max: make([]float32, d)}
	copy(cb.Min, data[:d])
	copy(cb.Max, data[:d])
	for i := 1; i < n; i++ {
		row := data[i*d : (i+1)*d]
		for j := 0; j < d; j++ {
			if row[j] < cb.Min[j] {
				cb.Min[j] = row[j]
			}
			if row[j] > cb.Max[j] {
				cb.Max[j] = row[j]
			}
		}
	}
	return cb, nil
}

// Encode maps a raw fp32 vector to D uint8 codes (spec 09 §2.4). A degenerate
// (zero-width) dimension encodes to 0.
func (c *SQCodebook) Encode(vec []float32, out []byte) {
	for i := 0; i < c.D; i++ {
		r := c.Max[i] - c.Min[i]
		if r < 1e-12 {
			out[i] = 0
			continue
		}
		v := (vec[i] - c.Min[i]) / r * 255.0
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		out[i] = byte(uint8(v))
	}
}

// Decode reconstructs an approximate fp32 vector from D uint8 codes.
func (c *SQCodebook) Decode(code []byte, out []float32) {
	for i := 0; i < c.D; i++ {
		t := float32(code[i]) / 255.0
		out[i] = c.Min[i] + t*(c.Max[i]-c.Min[i])
	}
}

// SQQuantizer adapts an SQ codebook to the Quantizer interface. SQ has no ADC
// table: the search path decodes a candidate and computes the exact metric, the
// same way rerank does (spec 09 §9.5), so NewADCTable returns nil.
type SQQuantizer struct{ cb *SQCodebook }

// NewSQQuantizer wraps a trained SQ codebook.
func NewSQQuantizer(cb *SQCodebook) *SQQuantizer { return &SQQuantizer{cb: cb} }

func (q *SQQuantizer) CodeSize() int                  { return q.cb.D }
func (q *SQQuantizer) Dim() int                       { return q.cb.D }
func (q *SQQuantizer) CodecKind() CodecKind           { return CodecSQ }
func (q *SQQuantizer) Encode(vec []float32, c []byte) { q.cb.Encode(vec, c) }
func (q *SQQuantizer) Decode(c []byte, out []float32) { q.cb.Decode(c, out) }
func (q *SQQuantizer) NewADCTable(query []float32, metric distance.Metric) ADCTable {
	return nil
}
