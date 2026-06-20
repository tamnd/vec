package quant

import (
	"math"
	"math/rand"

	"github.com/tamnd/vector/distance"
)

// PQCodebook stores the m per-subspace k-means codebooks for product
// quantization (spec 09 §3.4). A vector is split into M contiguous subvectors of
// Ds = D/M dimensions; each subvector is replaced by the index of its nearest
// centroid among Ksub = 2^Nbits, so a code is M bytes regardless of D.
type PQCodebook struct {
	D     int // original dimension (divisible by M)
	M     int // number of subspaces
	Ksub  int // centroids per subspace = 2^Nbits
	Nbits int // bits per code (8 -> 256 centroids)
	Ds    int // subvector dimension = D/M
	// Centroids[j] is subspace j's codebook, Ksub rows of Ds columns, row-major.
	Centroids [][]float32
}

// defaultKMeansSeed makes PQ/OPQ training reproducible: a codebook is a pure
// function of its training sample, which is what lets vec treat it as immutable
// (spec 09 §8.3).
const defaultKMeansSeed = 0x5EED

// TrainPQ fits a PQ codebook to a training sample (spec 09 §3.3). data is n rows
// of d columns, row-major. m must divide d. Each subspace is trained with an
// independent k-means over its slice of the sample. iters is the Lloyd iteration
// cap (25-50 is typical).
func TrainPQ(data []float32, n, d, m, nbits, iters int) (*PQCodebook, error) {
	if n == 0 {
		return nil, ErrEmptyTraining
	}
	if d%m != 0 {
		return nil, ErrNotDivisible
	}
	if !isFinite(data[:n*d]) {
		return nil, ErrNonFinite
	}
	ds := d / m
	ksub := 1 << nbits
	cb := &PQCodebook{D: d, M: m, Ksub: ksub, Nbits: nbits, Ds: ds, Centroids: make([][]float32, m)}

	// Extract each subspace's slice contiguously and run k-means on it. The rng is
	// seeded per subspace deterministically so training is reproducible.
	sub := make([]float32, n*ds)
	for j := 0; j < m; j++ {
		for i := 0; i < n; i++ {
			copy(sub[i*ds:(i+1)*ds], data[i*d+j*ds:i*d+(j+1)*ds])
		}
		rng := rand.New(rand.NewSource(int64(defaultKMeansSeed) + int64(j)))
		cb.Centroids[j] = kmeans(sub, n, ds, ksub, iters, rng)
	}
	return cb, nil
}

// Encode maps a raw fp32 vector to an M-byte PQ code by finding each subvector's
// nearest centroid (spec 09 §3.4).
func (c *PQCodebook) Encode(vec []float32, code []byte) {
	for j := 0; j < c.M; j++ {
		s := vec[j*c.Ds : (j+1)*c.Ds]
		best, bestD := 0, float32(math.MaxFloat32)
		cents := c.Centroids[j]
		for q := 0; q < c.Ksub; q++ {
			d := distance.L2SquaredFloat32(s, cents[q*c.Ds:(q+1)*c.Ds])
			if d < bestD {
				bestD, best = d, q
			}
		}
		code[j] = byte(best)
	}
}

// Decode reconstructs an approximate fp32 vector by concatenating the chosen
// centroids (spec 09 §3.4).
func (c *PQCodebook) Decode(code []byte, out []float32) {
	for j := 0; j < c.M; j++ {
		q := int(code[j])
		copy(out[j*c.Ds:], c.Centroids[j][q*c.Ds:(q+1)*c.Ds])
	}
}

// PQQuantizer adapts a PQ codebook to the Quantizer interface.
type PQQuantizer struct{ cb *PQCodebook }

// NewPQQuantizer wraps a trained PQ codebook.
func NewPQQuantizer(cb *PQCodebook) *PQQuantizer { return &PQQuantizer{cb: cb} }

func (q *PQQuantizer) CodeSize() int                  { return q.cb.M }
func (q *PQQuantizer) Dim() int                       { return q.cb.D }
func (q *PQQuantizer) CodecKind() CodecKind           { return CodecPQ }
func (q *PQQuantizer) Encode(vec []float32, c []byte) { q.cb.Encode(vec, c) }
func (q *PQQuantizer) Decode(c []byte, out []float32) { q.cb.Decode(c, out) }

// NewADCTable builds the query-to-centroid table for ADC search (spec 09 §9.1).
func (q *PQQuantizer) NewADCTable(query []float32, metric distance.Metric) ADCTable {
	return buildPQADC(query, q.cb, metric)
}

// pqADCTable is a precomputed M*Ksub table summed per code at search time
// (spec 09 §9.1).
type pqADCTable struct {
	table []float32 // M*Ksub, row-major by subspace
	M     int
	Ksub  int
}

// buildPQADC precomputes T[j][c] = distFn(q_j, centroid_j[c]) for the metric
// (spec 09 §9.1). Inner product and cosine negate so the heap stays a min-heap.
func buildPQADC(q []float32, cb *PQCodebook, metric distance.Metric) *pqADCTable {
	t := &pqADCTable{table: make([]float32, cb.M*cb.Ksub), M: cb.M, Ksub: cb.Ksub}
	dot := metric == distance.Dot || metric == distance.Cosine
	for j := 0; j < cb.M; j++ {
		qj := q[j*cb.Ds : (j+1)*cb.Ds]
		cents := cb.Centroids[j]
		base := j * cb.Ksub
		for c := 0; c < cb.Ksub; c++ {
			cent := cents[c*cb.Ds : (c+1)*cb.Ds]
			if dot {
				t.table[base+c] = -distance.DotFloat32(qj, cent)
			} else {
				t.table[base+c] = distance.L2SquaredFloat32(qj, cent)
			}
		}
	}
	return t
}

// Distance sums the table entries selected by the code (spec 09 §9.1): M lookups
// and M additions from a cache-resident table.
func (t *pqADCTable) Distance(code []byte) float32 {
	var d float32
	for j := 0; j < t.M; j++ {
		d += t.table[j*t.Ksub+int(code[j])]
	}
	return d
}
