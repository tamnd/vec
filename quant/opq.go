package quant

import (
	"math"
	"math/rand"

	"github.com/tamnd/vec/distance"
)

// OPQCodebook wraps a PQ codebook with a learned orthogonal rotation R applied
// before quantization (spec 09 §4.2). The rotation distributes variance evenly
// across subspaces so quantization error is uniform, lifting recall 5-15 points
// over plain PQ at the same M and Nbits.
type OPQCodebook struct {
	PQ *PQCodebook
	R  []float32 // D*D orthogonal rotation, row-major
	D  int
}

// TrainOPQ learns the rotation and PQ codebook by alternating minimization
// (spec 09 §4.2): fix R and fit PQ on the rotated sample, then fix PQ and update R
// as the orthogonal Procrustes solution that best aligns the rotated vectors with
// their reconstructions. outerIters is the number of alternations (20-50 typical);
// kmIters is the inner Lloyd cap.
func TrainOPQ(data []float32, n, d, m, nbits, outerIters, kmIters int) (*OPQCodebook, error) {
	if n == 0 {
		return nil, ErrEmptyTraining
	}
	if d%m != 0 {
		return nil, ErrNotDivisible
	}
	if !isFinite(data[:n*d]) {
		return nil, ErrNonFinite
	}

	// Start from a random orthogonal rotation so the first PQ fit is not axis-aligned.
	r := randomOrthogonal(d, rand.New(rand.NewSource(int64(defaultKMeansSeed))))

	rotated := make([]float32, n*d)
	recon := make([]float32, d)
	code := make([]byte, m)
	cross := make([]float64, d*d)

	var cb *PQCodebook
	for it := 0; it < outerIters; it++ {
		// Step 1: rotate the sample by the current R, then fit PQ on it.
		for i := 0; i < n; i++ {
			matVecMul(r, data[i*d:(i+1)*d], rotated[i*d:(i+1)*d], d)
		}
		var err error
		cb, err = TrainPQ(rotated, n, d, m, nbits, kmIters)
		if err != nil {
			return nil, err
		}
		if it == outerIters-1 {
			break // R is fixed for the final codebook
		}

		// Step 2: accumulate the cross-covariance A = sum_i recon_i (rotated_i)^T... but
		// Procrustes for min ||R x - y|| uses A = sum_i y_i x_i^T with y = reconstruction
		// in the rotated frame and x = the original vector. We want the R that maps
		// originals onto reconstructions, so accumulate recon_i * data_i^T.
		for i := range cross {
			cross[i] = 0
		}
		for i := 0; i < n; i++ {
			rot := rotated[i*d : (i+1)*d]
			cb.Encode(rot, code)
			cb.Decode(code, recon)
			x := data[i*d : (i+1)*d]
			for a := 0; a < d; a++ {
				ra := float64(recon[a])
				base := a * d
				for b := 0; b < d; b++ {
					cross[base+b] += ra * float64(x[b])
				}
			}
		}
		crossF := make([]float32, d*d)
		for i := range cross {
			crossF[i] = float32(cross[i])
		}
		r = procrustes(crossF, d)
	}
	return &OPQCodebook{PQ: cb, R: r, D: d}, nil
}

// Encode rotates the vector by R then PQ-encodes it (spec 09 §4.3).
func (c *OPQCodebook) Encode(vec []float32, code []byte) {
	rotated := make([]float32, c.D)
	matVecMul(c.R, vec, rotated, c.D)
	c.PQ.Encode(rotated, code)
}

// Decode PQ-decodes then un-rotates by R^T (spec 09 §4.3).
func (c *OPQCodebook) Decode(code []byte, out []float32) {
	rotated := make([]float32, c.D)
	c.PQ.Decode(code, rotated)
	matVecMulT(c.R, rotated, out, c.D)
}

// OPQQuantizer adapts an OPQ codebook to the Quantizer interface. ADC is shared
// with PQ; the only difference is the query is rotated before the table is built
// (spec 09 §9.4).
type OPQQuantizer struct{ cb *OPQCodebook }

// NewOPQQuantizer wraps a trained OPQ codebook.
func NewOPQQuantizer(cb *OPQCodebook) *OPQQuantizer { return &OPQQuantizer{cb: cb} }

func (q *OPQQuantizer) CodeSize() int                  { return q.cb.PQ.M }
func (q *OPQQuantizer) Dim() int                       { return q.cb.D }
func (q *OPQQuantizer) CodecKind() CodecKind           { return CodecOPQ }
func (q *OPQQuantizer) Encode(vec []float32, c []byte) { q.cb.Encode(vec, c) }
func (q *OPQQuantizer) Decode(c []byte, out []float32) { q.cb.Decode(c, out) }

// NewADCTable rotates the query by R then builds the PQ ADC table on the rotated
// query (spec 09 §9.4).
func (q *OPQQuantizer) NewADCTable(query []float32, metric distance.Metric) ADCTable {
	rq := make([]float32, q.cb.D)
	matVecMul(q.cb.R, query, rq, q.cb.D)
	return buildPQADC(rq, q.cb.PQ, metric)
}

// randomOrthogonal returns a random D*D orthogonal matrix (row-major) by
// Gram-Schmidt on a random Gaussian matrix. It is the OPQ rotation seed.
func randomOrthogonal(d int, rng *rand.Rand) []float32 {
	m := make([]float64, d*d)
	for i := range m {
		m[i] = rng.NormFloat64()
	}
	// Modified Gram-Schmidt on rows.
	for i := 0; i < d; i++ {
		ri := m[i*d : (i+1)*d]
		for k := 0; k < i; k++ {
			rk := m[k*d : (k+1)*d]
			var dot float64
			for j := 0; j < d; j++ {
				dot += ri[j] * rk[j]
			}
			for j := 0; j < d; j++ {
				ri[j] -= dot * rk[j]
			}
		}
		var norm float64
		for j := 0; j < d; j++ {
			norm += ri[j] * ri[j]
		}
		norm = math.Sqrt(norm)
		if norm < 1e-12 {
			// Degenerate row: replace with a unit basis vector.
			for j := 0; j < d; j++ {
				ri[j] = 0
			}
			ri[i] = 1
			norm = 1
		}
		for j := 0; j < d; j++ {
			ri[j] /= norm
		}
	}
	out := make([]float32, d*d)
	for i := range m {
		out[i] = float32(m[i])
	}
	return out
}
