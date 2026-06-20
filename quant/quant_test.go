package quant

import (
	"math"
	"math/rand"
	"testing"

	"github.com/tamnd/vec/distance"
)

// makeData returns n random d-dim vectors drawn from k Gaussian clusters, so PQ
// and SQ have real structure to recover rather than uniform noise.
func makeData(n, d, k int, seed int64) []float32 {
	rng := rand.New(rand.NewSource(seed))
	centers := make([]float32, k*d)
	for i := range centers {
		centers[i] = float32(rng.NormFloat64()) * 4
	}
	data := make([]float32, n*d)
	for i := 0; i < n; i++ {
		c := (i % k) * d
		for j := 0; j < d; j++ {
			data[i*d+j] = centers[c+j] + float32(rng.NormFloat64())
		}
	}
	return data
}

func l2(a, b []float32) float32 {
	var s float32
	for i := range a {
		d := a[i] - b[i]
		s += d * d
	}
	return float32(math.Sqrt(float64(s)))
}

func TestFlatRoundTrip(t *testing.T) {
	q := &FlatQuantizer{D: 8}
	if q.CodeSize() != 32 {
		t.Fatalf("CodeSize = %d, want 32", q.CodeSize())
	}
	vec := []float32{1, -2, 3.5, 0, 100, -0.25, 7, 8}
	code := make([]byte, q.CodeSize())
	q.Encode(vec, code)
	out := make([]float32, q.Dim())
	q.Decode(code, out)
	for i := range vec {
		if out[i] != vec[i] {
			t.Fatalf("flat decode[%d] = %v, want %v", i, out[i], vec[i])
		}
	}
	adc := q.NewADCTable(vec, distance.L2Squared)
	if adc == nil {
		t.Fatal("flat NewADCTable returned nil")
	}
	if d := adc.Distance(code); d > 1e-4 {
		t.Fatalf("flat self-distance = %v, want ~0", d)
	}
}

func TestSQRoundTripAndError(t *testing.T) {
	const n, d = 500, 16
	data := makeData(n, d, 5, 1)
	cb, err := TrainSQ(data, n, d)
	if err != nil {
		t.Fatal(err)
	}
	q := NewSQQuantizer(cb)
	code := make([]byte, q.CodeSize())
	out := make([]float32, d)
	var maxErr float32
	for i := 0; i < n; i++ {
		v := data[i*d : (i+1)*d]
		q.Encode(v, code)
		q.Decode(code, out)
		if e := l2(v, out); e > maxErr {
			maxErr = e
		}
	}
	// 8-bit per-dim SQ should reconstruct a unit-scale Gaussian to well under 1.0.
	if maxErr > 1.0 {
		t.Fatalf("SQ max reconstruction error = %v, want < 1.0", maxErr)
	}
}

func TestSQRejectsNonFinite(t *testing.T) {
	data := []float32{1, 2, float32(math.Inf(1)), 4}
	if _, err := TrainSQ(data, 2, 2); err != ErrNonFinite {
		t.Fatalf("err = %v, want ErrNonFinite", err)
	}
	if _, err := TrainSQ(nil, 0, 2); err != ErrEmptyTraining {
		t.Fatalf("err = %v, want ErrEmptyTraining", err)
	}
}

func TestPQTrainEncodeADC(t *testing.T) {
	const n, d, m, nbits = 1000, 16, 4, 8
	data := makeData(n, d, 10, 2)
	cb, err := TrainPQ(data, n, d, m, nbits, 25)
	if err != nil {
		t.Fatal(err)
	}
	q := NewPQQuantizer(cb)
	if q.CodeSize() != m {
		t.Fatalf("PQ CodeSize = %d, want %d", q.CodeSize(), m)
	}

	// ADC distance from a query to a coded point should track the true L2Squared.
	query := data[:d]
	adc := q.NewADCTable(query, distance.L2Squared)
	code := make([]byte, m)
	out := make([]float32, d)
	var maxRel float64
	for i := 0; i < 50; i++ {
		v := data[i*d : (i+1)*d]
		cb.Encode(v, code)
		approx := adc.Distance(code)
		cb.Decode(code, out)
		exact := distance.L2SquaredFloat32(query, out)
		if exact > 1 {
			rel := math.Abs(float64(approx-exact)) / float64(exact)
			if rel > maxRel {
				maxRel = rel
			}
		}
	}
	// ADC equals the distance to the decoded centroid by construction.
	if maxRel > 1e-3 {
		t.Fatalf("ADC vs decoded-exact relative error = %v, want ~0", maxRel)
	}
}

func TestPQNotDivisible(t *testing.T) {
	data := makeData(10, 15, 2, 3)
	if _, err := TrainPQ(data, 10, 15, 4, 8, 5); err != ErrNotDivisible {
		t.Fatalf("err = %v, want ErrNotDivisible", err)
	}
}

func TestOPQImprovesOrMatchesPQ(t *testing.T) {
	const n, d, m, nbits = 800, 16, 4, 8
	data := makeData(n, d, 8, 4)
	pq, err := TrainPQ(data, n, d, m, nbits, 25)
	if err != nil {
		t.Fatal(err)
	}
	opq, err := TrainOPQ(data, n, d, m, nbits, 10, 25)
	if err != nil {
		t.Fatal(err)
	}

	pqErr := codecMSE(pq, data, n, d)
	opqErr := codecMSE(opq, data, n, d)
	// OPQ should not be materially worse than PQ on the same config; the rotation is
	// learned to reduce reconstruction error.
	if opqErr > pqErr*1.25 {
		t.Fatalf("OPQ MSE %v noticeably worse than PQ MSE %v", opqErr, pqErr)
	}
}

type encoder interface {
	Encode(vec []float32, code []byte)
	Decode(code []byte, out []float32)
}

func codecMSE(c encoder, data []float32, n, d int) float64 {
	// Determine code size by encoding once into a generously sized buffer.
	buf := make([]byte, d*4)
	out := make([]float32, d)
	var sum float64
	for i := 0; i < n; i++ {
		v := data[i*d : (i+1)*d]
		c.Encode(v, buf)
		c.Decode(buf, out)
		for j := 0; j < d; j++ {
			e := float64(v[j] - out[j])
			sum += e * e
		}
	}
	return sum / float64(n*d)
}

func TestBinaryRoundTrip(t *testing.T) {
	q := NewBinaryQuantizer(10)
	if q.CodeSize() != 2 {
		t.Fatalf("binary CodeSize = %d, want 2", q.CodeSize())
	}
	vec := []float32{1, -1, 2, -2, 0.5, -0.5, 3, -3, 0.1, -0.1}
	code := make([]byte, q.CodeSize())
	q.Encode(vec, code)
	// Bit i set iff vec[i] >= 0: pattern 1010101010 -> 0xAA, 0x80.
	if code[0] != 0xAA {
		t.Fatalf("binary code[0] = %#x, want 0xAA", code[0])
	}
	if code[1]&0x80 == 0 || code[1]&0x40 != 0 {
		t.Fatalf("binary code[1] = %#x, want bit8 set bit9 clear", code[1])
	}
	if q.NewADCTable(vec, distance.Hamming) != nil {
		t.Fatal("binary NewADCTable should be nil")
	}
}

func TestBinaryTrainedThreshold(t *testing.T) {
	// Two dims with means 10 and -10; a vector at the means encodes both bits set
	// (>= threshold), and one below both means encodes both clear.
	data := []float32{11, -9, 9, -11}
	q, err := TrainBinary(data, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(q.Threshold[0]-10)) > 1e-4 || math.Abs(float64(q.Threshold[1]+10)) > 1e-4 {
		t.Fatalf("thresholds = %v, want [10 -10]", q.Threshold)
	}
	code := make([]byte, q.CodeSize())
	q.Encode([]float32{20, 0}, code)
	if code[0]&0xC0 != 0xC0 {
		t.Fatalf("code = %#x, want both top bits set", code[0])
	}
}

func TestRaBitQRoundTrip(t *testing.T) {
	q := NewRaBitQQuantizer(8, false)
	if q.CodeSize() != 1 {
		t.Fatalf("rabitq CodeSize = %d, want 1", q.CodeSize())
	}
	vec := []float32{1, -1, 1, -1, 1, -1, 1, -1}
	code := make([]byte, q.CodeSize())
	q.Encode(vec, code)
	if code[0] != 0xAA {
		t.Fatalf("rabitq code = %#x, want 0xAA", code[0])
	}
	out := make([]float32, 8)
	q.Decode(code, out)
	scale := float32(1 / math.Sqrt(8))
	if math.Abs(float64(out[0]-scale)) > 1e-6 || math.Abs(float64(out[1]+scale)) > 1e-6 {
		t.Fatalf("rabitq decode = %v, want +/-%v", out[:2], scale)
	}
}

func TestRaBitQNormMode(t *testing.T) {
	q := NewRaBitQQuantizer(8, true)
	if q.CodeSize() != 5 {
		t.Fatalf("rabitq+norm CodeSize = %d, want 5", q.CodeSize())
	}
	vec := []float32{3, 4, 0, 0, 0, 0, 0, 0} // norm 5
	code := make([]byte, q.CodeSize())
	q.Encode(vec, code)
	if got := getFloat32(code[1:]); math.Abs(float64(got-5)) > 1e-5 {
		t.Fatalf("stored norm = %v, want 5", got)
	}
}

func TestCodebookIORoundTrip(t *testing.T) {
	const n, d, m, nbits = 400, 16, 4, 8
	data := makeData(n, d, 6, 7)

	sq, _ := TrainSQ(data, n, d)
	sq2, err := UnmarshalSQ(MarshalSQ(sq))
	if err != nil {
		t.Fatal(err)
	}
	if sq2.D != sq.D || sq2.Bits != sq.Bits {
		t.Fatal("SQ header mismatch after round trip")
	}
	for i := 0; i < d; i++ {
		if sq2.Min[i] != sq.Min[i] || sq2.Max[i] != sq.Max[i] {
			t.Fatalf("SQ range[%d] mismatch", i)
		}
	}

	pq, _ := TrainPQ(data, n, d, m, nbits, 20)
	pq2, err := UnmarshalPQ(MarshalPQ(pq))
	if err != nil {
		t.Fatal(err)
	}
	for j := 0; j < m; j++ {
		for i := range pq.Centroids[j] {
			if pq2.Centroids[j][i] != pq.Centroids[j][i] {
				t.Fatalf("PQ centroid[%d][%d] mismatch", j, i)
			}
		}
	}

	opq, _ := TrainOPQ(data, n, d, m, nbits, 5, 20)
	opq2, err := UnmarshalOPQ(MarshalOPQ(opq))
	if err != nil {
		t.Fatal(err)
	}
	for i := range opq.R {
		if opq2.R[i] != opq.R[i] {
			t.Fatalf("OPQ rotation[%d] mismatch", i)
		}
	}
}

func TestCodebookIOBadMagic(t *testing.T) {
	if _, err := UnmarshalSQ([]byte{0, 0, 0, 0}); err != ErrBadCodebook {
		t.Fatalf("err = %v, want ErrBadCodebook", err)
	}
	if _, err := UnmarshalPQ(MarshalSQ(&SQCodebook{D: 1, Bits: 8, Min: []float32{0}, Max: []float32{1}})); err != ErrBadCodebook {
		t.Fatalf("PQ on SQ page err = %v, want ErrBadCodebook", err)
	}
}

func TestOrthogonalRotationIsOrthogonal(t *testing.T) {
	const d = 12
	r := randomOrthogonal(d, rand.New(rand.NewSource(9)))
	// R R^T should be the identity.
	for i := 0; i < d; i++ {
		for j := 0; j < d; j++ {
			var s float32
			for k := 0; k < d; k++ {
				s += r[i*d+k] * r[j*d+k]
			}
			want := float32(0)
			if i == j {
				want = 1
			}
			if math.Abs(float64(s-want)) > 1e-4 {
				t.Fatalf("R R^T[%d][%d] = %v, want %v", i, j, s, want)
			}
		}
	}
}
