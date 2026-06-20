package distance

import (
	"math"
	"math/rand"
	"testing"
	"unsafe"
)

// dims exercises non-multiple-of-16 lengths, aligned and unaligned tails, and the
// dimensions vec is benchmarked at (spec 09 §12.4).
var dims = []int{1, 7, 15, 16, 64, 128, 256, 512, 768, 1024, 1536, 3072}

func randVec(r *rand.Rand, d int) []float32 {
	v := make([]float32, d)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return v
}

func TestL2SquaredMatchesNaiveDouble(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, d := range dims {
		a, b := randVec(r, d), randVec(r, d)
		// Reference in float64 to bound the fp32 kernel's associativity error.
		var ref float64
		for i := range a {
			diff := float64(a[i]) - float64(b[i])
			ref += diff * diff
		}
		got := L2SquaredFloat32(a, b)
		tol := 4 * 1.19e-7 * float64(d) * (ref + 1)
		if math.Abs(float64(got)-ref) > tol {
			t.Fatalf("d=%d L2Sq=%v ref=%v tol=%v", d, got, ref, tol)
		}
	}
}

func TestDotMatchesNaiveDouble(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for _, d := range dims {
		a, b := randVec(r, d), randVec(r, d)
		var ref float64
		for i := range a {
			ref += float64(a[i]) * float64(b[i])
		}
		got := DotFloat32(a, b)
		tol := 4 * 1.19e-7 * float64(d) * (math.Abs(ref) + 1)
		if math.Abs(float64(got)-ref) > tol {
			t.Fatalf("d=%d dot=%v ref=%v tol=%v", d, got, ref, tol)
		}
	}
}

func TestCosineIdenticalIsZero(t *testing.T) {
	a := []float32{1, 2, 3, 4}
	if d := CosineDistanceFloat32(a, a); math.Abs(float64(d)) > 1e-6 {
		t.Fatalf("cosine(a,a) = %v, want 0", d)
	}
}

func TestCosineOrthogonalIsOne(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if d := CosineDistanceFloat32(a, b); math.Abs(float64(d)-1) > 1e-6 {
		t.Fatalf("cosine(a,b) = %v, want 1", d)
	}
}

func TestCosineZeroVectorIsMaxDistance(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	if d := CosineDistanceFloat32(a, b); d != 1.0 {
		t.Fatalf("cosine with zero vector = %v, want 1.0", d)
	}
}

func TestCosineNormalizedFastPathMatches(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for _, d := range dims {
		a, b := randVec(r, d), randVec(r, d)
		normalize(a)
		normalize(b)
		full := CosineDistanceFloat32(a, b)
		fast := CosineNormalized(a, b)
		if math.Abs(float64(full-fast)) > 1e-4 {
			t.Fatalf("d=%d normalized cosine fast=%v full=%v", d, fast, full)
		}
	}
}

func normalize(v []float32) {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
}

func TestInt8KernelsMatchInt(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	for _, d := range dims {
		a := make([]int8, d)
		b := make([]int8, d)
		for i := range a {
			a[i] = int8(r.Intn(256) - 128)
			b[i] = int8(r.Intn(256) - 128)
		}
		var l2, dot int32
		for i := range a {
			diff := int32(a[i]) - int32(b[i])
			l2 += diff * diff
			dot += int32(a[i]) * int32(b[i])
		}
		if got := L2SquaredInt8(a, b); got != l2 {
			t.Fatalf("d=%d int8 L2Sq=%d want %d", d, got, l2)
		}
		if got := DotInt8(a, b); got != dot {
			t.Fatalf("d=%d int8 dot=%d want %d", d, got, dot)
		}
	}
}

func TestFp16RoundTrip(t *testing.T) {
	cases := []float32{0, 1, -1, 0.5, -0.5, 3.14159, 100, -100, 65504} // 65504 = max fp16
	for _, v := range cases {
		got := DecodeFloat16(EncodeFloat16(v))
		// fp16 has ~3 significant digits; bound the relative error.
		if v == 0 {
			if got != 0 {
				t.Fatalf("fp16 round-trip 0 = %v", got)
			}
			continue
		}
		rel := math.Abs(float64(got-v) / float64(v))
		if rel > 1e-3 {
			t.Fatalf("fp16 round-trip %v = %v rel=%v", v, got, rel)
		}
	}
}

func TestFp16DistanceApproximatesFp32(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	d := 256
	a32, b32 := randVec(r, d), randVec(r, d)
	a16 := make([]uint16, d)
	b16 := make([]uint16, d)
	EncodeFloat16Slice(a16, a32)
	EncodeFloat16Slice(b16, b32)
	ref := L2SquaredFloat32(a32, b32)
	got := L2SquaredFloat16(a16, b16)
	if rel := math.Abs(float64(got-ref) / float64(ref)); rel > 1e-2 {
		t.Fatalf("fp16 L2Sq=%v fp32=%v rel=%v", got, ref, rel)
	}
}

func TestHammingDistance(t *testing.T) {
	a := []byte{0b10101010, 0b11110000}
	b := []byte{0b01010101, 0b00001111}
	// Every bit differs: 16.
	if got := HammingDistance(a, b); got != 16 {
		t.Fatalf("hamming = %d, want 16", got)
	}
	if got := HammingDistance(a, a); got != 0 {
		t.Fatalf("hamming(a,a) = %d, want 0", got)
	}
}

func TestJaccardDistance(t *testing.T) {
	a := []byte{0b11110000}
	b := []byte{0b00111100}
	// A&B = 0b00110000 (2 bits), A|B = 0b11111100 (6 bits): 1 - 2/6 = 0.6667.
	got := JaccardDistance(a, b)
	if math.Abs(float64(got)-(1-2.0/6.0)) > 1e-6 {
		t.Fatalf("jaccard = %v, want %v", got, 1-2.0/6.0)
	}
	// Two empty codes are identical.
	if got := JaccardDistance([]byte{0}, []byte{0}); got != 0 {
		t.Fatalf("jaccard empty = %v, want 0", got)
	}
}

func TestRegistryDispatch(t *testing.T) {
	// fp32 L2Sq through the byte-level registry matches the typed kernel.
	d := 64
	r := rand.New(rand.NewSource(6))
	a, b := randVec(r, d), randVec(r, d)
	fn, ok := Lookup(L2Squared, Float32)
	if !ok {
		t.Fatal("no fp32 L2Sq kernel registered")
	}
	ab := float32SliceAsBytes(a)
	bb := float32SliceAsBytes(b)
	if got, want := fn(ab, bb, d), L2SquaredFloat32(a, b); got != want {
		t.Fatalf("registry L2Sq=%v typed=%v", got, want)
	}

	// Dot through the registry is negated (larger similarity = smaller distance).
	dotFn, _ := Lookup(Dot, Float32)
	if got, want := dotFn(ab, bb, d), -DotFloat32(a, b); got != want {
		t.Fatalf("registry dot=%v want %v", got, want)
	}
}

func TestUnsupportedCombinationAbsent(t *testing.T) {
	if _, ok := Lookup(Hamming, Float32); ok {
		t.Fatal("hamming over fp32 should not be registered")
	}
	if _, ok := Lookup(Cosine, Bit); ok {
		t.Fatal("cosine over binary should not be registered")
	}
}

func TestActiveTierIsComplete(t *testing.T) {
	// Whatever tier is active, the scalar fallback guarantees the core fp32
	// metrics are present.
	for _, m := range []Metric{L2Squared, L2, Cosine, Dot} {
		if _, ok := Lookup(m, Float32); !ok {
			t.Fatalf("metric %v missing for fp32 at tier %v", m, ActiveTier())
		}
	}
}

func float32SliceAsBytes(v []float32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&v[0])), len(v)*4)
}

func BenchmarkL2SquaredFp32D768(b *testing.B) {
	r := rand.New(rand.NewSource(7))
	x, y := randVec(r, 768), randVec(r, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = L2SquaredFloat32(x, y)
	}
}

func BenchmarkDotFp32D768(b *testing.B) {
	r := rand.New(rand.NewSource(8))
	x, y := randVec(r, 768), randVec(r, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DotFloat32(x, y)
	}
}
