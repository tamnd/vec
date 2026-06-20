package distance

import "math"

// EncodeFloat16 converts a float32 to its IEEE 754 half-precision (fp16) bit
// pattern stored in a uint16 (spec 09 §7.2). Denormals flush to a signed zero;
// out-of-range magnitudes saturate to signed infinity. This is the scalar
// reference the F16C/NEON conversion kernels must match.
func EncodeFloat16(v float32) uint16 {
	b := math.Float32bits(v)
	sign := (b >> 16) & 0x8000
	exp := int((b>>23)&0xFF) - 127 + 15
	mant := (b >> 13) & 0x3FF
	if exp <= 0 {
		return uint16(sign) // flush to zero (denormals not represented)
	}
	if exp >= 31 {
		return uint16(sign | 0x7C00) // saturate to infinity
	}
	return uint16(sign | uint32(exp)<<10 | mant)
}

// DecodeFloat16 converts an fp16 bit pattern back to float32 (spec 09 §7.2). A
// zero exponent decodes to a signed zero (denormals flushed); a full exponent
// decodes to inf/nan.
func DecodeFloat16(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32((h >> 10) & 0x1F)
	mant := uint32(h & 0x3FF)
	if exp == 0 {
		return math.Float32frombits(sign) // zero or denormal -> signed zero
	}
	if exp == 31 {
		return math.Float32frombits(sign | 0x7F800000 | mant<<13) // inf/nan
	}
	return math.Float32frombits(sign | (exp+112)<<23 | mant<<13)
}

// EncodeFloat16Slice converts a float32 vector to its fp16 storage form.
func EncodeFloat16Slice(dst []uint16, src []float32) {
	for i, v := range src {
		dst[i] = EncodeFloat16(v)
	}
}

// DecodeFloat16Slice converts an fp16 storage vector back to float32.
func DecodeFloat16Slice(dst []float32, src []uint16) {
	for i, h := range src {
		dst[i] = DecodeFloat16(h)
	}
}
