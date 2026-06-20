package distance

import (
	"math"
	"unsafe"
)

// KernelSet holds the active typed kernel functions, chosen once at init by SIMD
// tier (spec 09 §11.2). Hot loops that already hold typed slices (the flat scan,
// HNSW traversal) call these directly; the byte-level registry below wraps them
// for the codec-generic path.
type KernelSet struct {
	L2SquaredFp32 func(a, b []float32) float32
	DotFp32       func(a, b []float32) float32
	CosineFp32    func(a, b []float32) float32
	L2SquaredInt8 func(a, b []int8) int32
	DotInt8       func(a, b []int8) int32
	L2SquaredFp16 func(a, b []uint16) float32
	DotFp16       func(a, b []uint16) float32
	HammingBytes  func(a, b []byte) int
	JaccardBytes  func(a, b []byte) float32
}

// scalarKernelSet is the always-complete pure-Go tier (spec 09 §11.3, §12.1). It
// is both the default active set and the fallback every higher tier defers to.
func scalarKernelSet() KernelSet {
	return KernelSet{
		L2SquaredFp32: L2SquaredFloat32,
		DotFp32:       DotFloat32,
		CosineFp32:    CosineDistanceFloat32,
		L2SquaredInt8: L2SquaredInt8,
		DotInt8:       DotInt8,
		L2SquaredFp16: L2SquaredFloat16,
		DotFp16:       DotFloat16,
		HammingBytes:  HammingDistance,
		JaccardBytes:  JaccardDistance,
	}
}

// activeTier is the SIMD tier selected at init; ActiveTier exposes it for the
// PRAGMA that reports the running kernel set (spec 09 §12.3).
var activeTier = detectSIMDTier()

// activeKernels is the live kernel set; all typed distance calls route through it.
var activeKernels = selectKernels(activeTier)

// ActiveTier returns the SIMD tier the running binary selected.
func ActiveTier() SIMDTier { return activeTier }

// Kernels returns the active typed kernel set.
func Kernels() KernelSet { return activeKernels }

// detectSIMDTier picks the best kernel tier for this CPU (spec 09 §12.3). The
// Go-assembly tiers are a later slice; until they register, this returns
// TierScalar, which the spec defines as the always-correct complete fallback.
// When the asm kernels land, this gains the cpu-feature probes (AVX-512F/BW/VL,
// AVX2+FMA, NEON ASIMD) and selectKernels grows the matching cases.
func detectSIMDTier() SIMDTier { return TierScalar }

// selectKernels returns the kernel set for a tier, falling back to scalar for any
// tier whose kernels are not yet wired (spec 09 §11.2, §12.1).
func selectKernels(tier SIMDTier) KernelSet {
	switch tier {
	default:
		return scalarKernelSet()
	}
}

// kernelRegistry maps (metric, element type) to the active byte-level kernel
// (spec 09 §12.1). It is the codec-generic dispatch the storage and index layers
// call when they hold raw segment bytes rather than typed slices.
var kernelRegistry = buildRegistry(activeKernels)

// buildRegistry wires every (metric, element type) vec supports to a DistanceFn
// over raw bytes, reinterpreting the bytes per element type. Unsupported
// combinations are simply absent; Lookup reports them.
func buildRegistry(k KernelSet) map[MetricKernelKey]DistanceFn {
	r := make(map[MetricKernelKey]DistanceFn)

	r[MetricKernelKey{L2Squared, Float32}] = func(a, b []byte, D int) float32 {
		return k.L2SquaredFp32(asFloat32(a, D), asFloat32(b, D))
	}
	r[MetricKernelKey{L2, Float32}] = func(a, b []byte, D int) float32 {
		return float32(math.Sqrt(float64(k.L2SquaredFp32(asFloat32(a, D), asFloat32(b, D)))))
	}
	r[MetricKernelKey{Dot, Float32}] = func(a, b []byte, D int) float32 {
		return -k.DotFp32(asFloat32(a, D), asFloat32(b, D))
	}
	r[MetricKernelKey{Cosine, Float32}] = func(a, b []byte, D int) float32 {
		return k.CosineFp32(asFloat32(a, D), asFloat32(b, D))
	}

	r[MetricKernelKey{L2Squared, Float16}] = func(a, b []byte, D int) float32 {
		return k.L2SquaredFp16(asUint16(a, D), asUint16(b, D))
	}
	r[MetricKernelKey{Dot, Float16}] = func(a, b []byte, D int) float32 {
		return -k.DotFp16(asUint16(a, D), asUint16(b, D))
	}

	r[MetricKernelKey{L2Squared, Int8}] = func(a, b []byte, D int) float32 {
		return float32(k.L2SquaredInt8(asInt8(a, D), asInt8(b, D)))
	}
	r[MetricKernelKey{Dot, Int8}] = func(a, b []byte, D int) float32 {
		return float32(-k.DotInt8(asInt8(a, D), asInt8(b, D)))
	}

	r[MetricKernelKey{Hamming, Bit}] = func(a, b []byte, D int) float32 {
		return float32(k.HammingBytes(a[:byteLen(D)], b[:byteLen(D)]))
	}
	r[MetricKernelKey{Jaccard, Bit}] = func(a, b []byte, D int) float32 {
		return k.JaccardBytes(a[:byteLen(D)], b[:byteLen(D)])
	}
	return r
}

// Lookup returns the byte-level kernel for (metric, elemType) and whether one is
// registered (spec 09 §12.1). A false ok means the combination is unsupported,
// which the catalog rejects at collection-create time.
func Lookup(metric Metric, elemType ElemType) (DistanceFn, bool) {
	fn, ok := kernelRegistry[MetricKernelKey{metric, elemType}]
	return fn, ok
}

// MustLookup returns the kernel for (metric, elemType) or panics if the
// combination is unsupported. Callers that validated the pair at catalog time use
// this on the hot path.
func MustLookup(metric Metric, elemType ElemType) DistanceFn {
	fn, ok := Lookup(metric, elemType)
	if !ok {
		panic("distance: no kernel for " + metric.String() + "/" + elemType.String())
	}
	return fn
}

// CosineNormalized returns 1 - dot(a,b) for pre-normalized unit-norm vectors, the
// fast cosine path that skips both norms and both sqrts (spec 09 §13.4). The
// collection schema's normalized flag selects this over CosineDistanceFloat32.
func CosineNormalized(a, b []float32) float32 {
	return 1 - activeKernels.DotFp32(a, b)
}

// byteLen returns the number of bytes a D-bit binary code occupies.
func byteLen(D int) int { return (D + 7) / 8 }

// asFloat32 reinterprets the first D float32 elements of a byte slice without
// copying (spec 09 §11.7). The segment store guarantees element alignment.
func asFloat32(b []byte, D int) []float32 {
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), D)
}

// asUint16 reinterprets the first D uint16 (fp16) elements of a byte slice.
func asUint16(b []byte, D int) []uint16 {
	return unsafe.Slice((*uint16)(unsafe.Pointer(&b[0])), D)
}

// asInt8 reinterprets the first D int8 elements of a byte slice.
func asInt8(b []byte, D int) []int8 {
	return unsafe.Slice((*int8)(unsafe.Pointer(&b[0])), D)
}
