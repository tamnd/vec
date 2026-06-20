package storage

import (
	"encoding/binary"
	"math"
)

// vecCodec converts between the in-memory float32 representation the Index SPI
// works in and the stored byte representation of a vector segment (spec 04 §18).
// fp32 is an identity copy; fp16/int8/binary are the compressed stored forms that
// FetchVector dequantizes on the fly (spec 04 §18.4-§18.6). Encode writes exactly
// rawBytes bytes; the segment pads the slot to its stride.
type vecCodec interface {
	rawBytes() int
	encode(vec []float32, dst []byte)
	decode(src []byte, dst []float32)
}

// codecFor returns the codec for a collection's element type (spec 04 §3.3).
func codecFor(elem ElemType, dims uint32, int8Scale float32) vecCodec {
	switch elem {
	case ElemFP16:
		return fp16Codec{dims: int(dims)}
	case ElemInt8:
		s := int8Scale
		if s == 0 {
			s = 1
		}
		return int8Codec{dims: int(dims), scale: s}
	case ElemBinary:
		return binaryCodec{dims: int(dims)}
	default:
		return fp32Codec{dims: int(dims)}
	}
}

// fp32Codec stores vectors verbatim as little-endian float32 (spec 04 §18.1).
type fp32Codec struct{ dims int }

func (c fp32Codec) rawBytes() int { return c.dims * 4 }

func (c fp32Codec) encode(vec []float32, dst []byte) {
	for i := 0; i < c.dims; i++ {
		binary.LittleEndian.PutUint32(dst[i*4:], math.Float32bits(vec[i]))
	}
}

func (c fp32Codec) decode(src []byte, dst []float32) {
	for i := 0; i < c.dims; i++ {
		dst[i] = math.Float32frombits(binary.LittleEndian.Uint32(src[i*4:]))
	}
}

// fp16Codec stores IEEE 754 binary16 (spec 04 §18.4). Conversion is lossy but
// round-trips values within fp16 precision.
type fp16Codec struct{ dims int }

func (c fp16Codec) rawBytes() int { return c.dims * 2 }

func (c fp16Codec) encode(vec []float32, dst []byte) {
	for i := 0; i < c.dims; i++ {
		binary.LittleEndian.PutUint16(dst[i*2:], float32ToHalf(vec[i]))
	}
}

func (c fp16Codec) decode(src []byte, dst []float32) {
	for i := 0; i < c.dims; i++ {
		dst[i] = halfToFloat32(binary.LittleEndian.Uint16(src[i*2:]))
	}
}

// int8Codec stores symmetric int8 scalar quantization (spec 04 §18.5): each
// element is round(v/scale) clamped to [-127, 127]; decode multiplies by scale.
type int8Codec struct {
	dims  int
	scale float32
}

func (c int8Codec) rawBytes() int { return c.dims }

func (c int8Codec) encode(vec []float32, dst []byte) {
	for i := 0; i < c.dims; i++ {
		q := int32(math.Round(float64(vec[i] / c.scale)))
		if q > 127 {
			q = 127
		}
		if q < -127 {
			q = -127
		}
		dst[i] = byte(int8(q))
	}
}

func (c int8Codec) decode(src []byte, dst []float32) {
	for i := 0; i < c.dims; i++ {
		dst[i] = float32(int8(src[i])) * c.scale
	}
}

// binaryCodec packs one sign bit per dimension (spec 04 §18.6): bit set when the
// element is positive. decode returns 1.0 for a set bit and 0.0 otherwise.
type binaryCodec struct{ dims int }

func (c binaryCodec) rawBytes() int { return (c.dims + 7) / 8 }

func (c binaryCodec) encode(vec []float32, dst []byte) {
	for i := range dst {
		dst[i] = 0
	}
	for i := 0; i < c.dims; i++ {
		if vec[i] > 0 {
			dst[i>>3] |= 1 << (uint(i) & 7)
		}
	}
}

func (c binaryCodec) decode(src []byte, dst []float32) {
	for i := 0; i < c.dims; i++ {
		if src[i>>3]&(1<<(uint(i)&7)) != 0 {
			dst[i] = 1
		} else {
			dst[i] = 0
		}
	}
}

// float32ToHalf converts a float32 to IEEE 754 binary16 (round-to-nearest-even).
func float32ToHalf(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16((b >> 16) & 0x8000)
	exp := int32((b>>23)&0xFF) - 127 + 15
	mant := b & 0x7FFFFF
	switch {
	case (b & 0x7FFFFFFF) == 0:
		return sign
	case exp >= 0x1F:
		// Overflow or Inf/NaN.
		if (b&0x7F800000) == 0x7F800000 && mant != 0 {
			return sign | 0x7E00 // NaN
		}
		return sign | 0x7C00 // Inf
	case exp <= 0:
		if exp < -10 {
			return sign
		}
		mant |= 0x800000
		shift := uint32(14 - exp)
		half := uint16(mant >> shift)
		if (mant>>(shift-1))&1 != 0 {
			half++
		}
		return sign | half
	default:
		half := sign | uint16(exp<<10) | uint16(mant>>13)
		if mant&0x1000 != 0 {
			half++
		}
		return half
	}
}

// halfToFloat32 converts IEEE 754 binary16 to float32.
func halfToFloat32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1F
	mant := uint32(h & 0x3FF)
	switch exp {
	case 0:
		if mant == 0 {
			return math.Float32frombits(sign)
		}
		// Subnormal: normalize.
		e := uint32(127 - 15 + 1)
		for mant&0x400 == 0 {
			mant <<= 1
			e--
		}
		mant &= 0x3FF
		return math.Float32frombits(sign | e<<23 | mant<<13)
	case 0x1F:
		if mant == 0 {
			return math.Float32frombits(sign | 0x7F800000)
		}
		return math.Float32frombits(sign | 0x7F800000 | mant<<13)
	default:
		return math.Float32frombits(sign | (exp-15+127)<<23 | mant<<13)
	}
}
