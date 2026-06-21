package vecpb

import (
	"encoding/binary"
	"math"
)

// EncodeVector packs a float32 slice into the little-endian IEEE 754 byte
// payload carried by Vector.data (spec 16 §2.2). The result is len(v)*4 bytes.
func EncodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// DecodeVector reverses EncodeVector. Trailing bytes that do not form a full
// float32 are ignored. A nil or empty input yields a nil slice.
func DecodeVector(b []byte) []float32 {
	n := len(b) / 4
	if n == 0 {
		return nil
	}
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
