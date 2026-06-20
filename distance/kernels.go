package distance

import (
	"math"
	"math/bits"
)

// This file holds the pure-Go scalar kernels (spec 09 §11.3). They are the
// specification and the correctness oracle: every SIMD variant must reproduce
// these results within the fp32 associativity tolerance (spec 09 §12.4). They
// accumulate left-to-right; SIMD variants accumulate in wider parallel chunks and
// so differ in the last few bits, which is why ranking ties break on point id,
// never on float equality (spec 09 §13.3).

// L2SquaredFloat32 returns the squared L2 distance between two fp32 vectors.
func L2SquaredFloat32(a, b []float32) float32 {
	var sum float32
	for i := range a {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// DotFloat32 returns the inner product of two fp32 vectors.
func DotFloat32(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// CosineDistanceFloat32 returns 1 - dot(a,b)/(|a|*|b|). A zero-norm vector yields
// the maximum distance 1.0 (spec 09 §11.3). For pre-normalized collections the
// dispatch uses the 1 - DotFloat32 fast path instead and skips the norms
// (spec 09 §13.4).
func CosineDistanceFloat32(a, b []float32) float32 {
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA < 1e-30 || normB < 1e-30 {
		return 1.0
	}
	return 1 - dot/(float32(math.Sqrt(float64(normA)))*float32(math.Sqrt(float64(normB))))
}

// L2SquaredInt8 returns the squared L2 distance over int8 vectors, accumulating
// in int32 to avoid overflow (spec 09 §11.1, §13.1). The maximum accumulated
// value at d=768 stays well within int32.
func L2SquaredInt8(a, b []int8) int32 {
	var sum int32
	for i := range a {
		d := int32(a[i]) - int32(b[i])
		sum += d * d
	}
	return sum
}

// DotInt8 returns the inner product over int8 vectors, accumulating in int32.
func DotInt8(a, b []int8) int32 {
	var sum int32
	for i := range a {
		sum += int32(a[i]) * int32(b[i])
	}
	return sum
}

// L2SquaredFloat16 returns the squared L2 distance over fp16 vectors, dequantizing
// each element to float32 on the fly (spec 09 §7.3).
func L2SquaredFloat16(a, b []uint16) float32 {
	var sum float32
	for i := range a {
		d := DecodeFloat16(a[i]) - DecodeFloat16(b[i])
		sum += d * d
	}
	return sum
}

// DotFloat16 returns the inner product over fp16 vectors, dequantizing on the fly.
func DotFloat16(a, b []uint16) float32 {
	var sum float32
	for i := range a {
		sum += DecodeFloat16(a[i]) * DecodeFloat16(b[i])
	}
	return sum
}

// HammingDistance returns the number of differing bits between two packed binary
// codes (spec 09 §6.3).
func HammingDistance(a, b []byte) int {
	n := 0
	for i := range a {
		n += bits.OnesCount8(a[i] ^ b[i])
	}
	return n
}

// JaccardDistance returns 1 - |A&B|/|A|B| over two packed binary codes
// (spec 09 §6.4). Two empty codes are identical (distance 0).
func JaccardDistance(a, b []byte) float32 {
	inter, union := 0, 0
	for i := range a {
		xb := a[i] ^ b[i]
		nb := a[i] | b[i]
		inter += bits.OnesCount8(^xb & nb) // bits set in both
		union += bits.OnesCount8(nb)       // bits set in either
	}
	if union == 0 {
		return 0
	}
	return 1 - float32(inter)/float32(union)
}
