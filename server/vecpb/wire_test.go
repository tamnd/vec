package vecpb

import (
	"bytes"
	"reflect"
	"testing"
)

// TestEncodeDecodeVectorRoundTrip checks the float32 payload helpers.
func TestEncodeDecodeVectorRoundTrip(t *testing.T) {
	in := []float32{0.1, -0.34, 0.56, 1e9, -1e-9, 0}
	b := EncodeVector(in)
	if len(b) != len(in)*4 {
		t.Fatalf("len = %d, want %d", len(b), len(in)*4)
	}
	out := DecodeVector(b)
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch: %v vs %v", in, out)
	}
}

// TestEncodeVectorKnownLayout pins the little-endian byte layout.
func TestEncodeVectorKnownLayout(t *testing.T) {
	// 1.0f32 = 0x3F800000, little-endian = 00 00 80 3F.
	// 2.0f32 = 0x40000000, little-endian = 00 00 00 40.
	got := EncodeVector([]float32{1.0, 2.0})
	want := []byte{0x00, 0x00, 0x80, 0x3F, 0x00, 0x00, 0x00, 0x40}
	if !bytes.Equal(got, want) {
		t.Fatalf("layout = % x, want % x", got, want)
	}
	if v := DecodeVector(want); !reflect.DeepEqual(v, []float32{1.0, 2.0}) {
		t.Fatalf("decode = %v", v)
	}
}

// TestDecodeVectorEmpty checks nil handling.
func TestDecodeVectorEmpty(t *testing.T) {
	if DecodeVector(nil) != nil {
		t.Fatal("nil input should decode to nil")
	}
	if DecodeVector([]byte{1, 2, 3}) != nil {
		t.Fatal("sub-float input should decode to nil")
	}
}

// TestVarintRoundTrip checks the low-level varint writer/reader.
func TestVarintRoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 300, 16384, 1 << 35, ^uint64(0)}
	for _, c := range cases {
		b := appendVarint(nil, c)
		r := reader{buf: b}
		got, err := r.readVarint()
		if err != nil {
			t.Fatalf("readVarint(%d): %v", c, err)
		}
		if got != c {
			t.Fatalf("varint round trip: got %d want %d", got, c)
		}
		if !r.done() {
			t.Fatalf("varint %d left trailing bytes", c)
		}
	}
}

// TestGoldenValueInt pins Value{IntVal:1} to its exact bytes.
func TestGoldenValueInt(t *testing.T) {
	v := &Value{Kind: ValueInt, IntVal: 1}
	got := v.Marshal()
	// field 1, wire 0 -> tag 0x08; varint value 1 -> 0x01.
	want := []byte{0x08, 0x01}
	if !bytes.Equal(got, want) {
		t.Fatalf("Value{IntVal:1} = % x, want % x", got, want)
	}
}

// TestGoldenVectorTwoFloats pins a Vector carrying two float32s.
func TestGoldenVectorTwoFloats(t *testing.T) {
	v := &Vector{Data: EncodeVector([]float32{1.0, 2.0})}
	got := v.Marshal()
	// field 1, wire 2 -> tag 0x0A; length 8 -> 0x08; then the 8 LE bytes.
	want := []byte{
		0x0A, 0x08,
		0x00, 0x00, 0x80, 0x3F,
		0x00, 0x00, 0x00, 0x40,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Vector = % x, want % x", got, want)
	}
}

// TestUnknownFieldSkipped checks forward compatibility: a message with extra
// fields of each wire type still decodes the known fields.
func TestUnknownFieldSkipped(t *testing.T) {
	var b []byte
	// known field 1 (Value.int_val) = 7
	b = appendVarintFieldAlways(b, 1, 7)
	// unknown varint field 9
	b = appendTag(b, 9, wireVarint)
	b = appendVarint(b, 99999)
	// unknown fixed64 field 10
	b = appendTag(b, 10, wireFixed64)
	b = append(b, 1, 2, 3, 4, 5, 6, 7, 8)
	// unknown length-delimited field 11
	b = appendTag(b, 11, wireBytes)
	b = appendVarint(b, 3)
	b = append(b, 'x', 'y', 'z')
	// unknown fixed32 field 12
	b = appendTag(b, 12, wireFixed32)
	b = append(b, 9, 9, 9, 9)

	v, err := UnmarshalValue(b)
	if err != nil {
		t.Fatalf("unmarshal with unknown fields: %v", err)
	}
	if v.Kind != ValueInt || v.IntVal != 7 {
		t.Fatalf("known field lost: %+v", v)
	}
}

// TestTruncatedErrors checks that short buffers report ErrTruncated.
func TestTruncatedErrors(t *testing.T) {
	// tag for a length-delimited field claiming 5 bytes but none present.
	b := appendTag(nil, 1, wireBytes)
	b = appendVarint(b, 5)
	if _, err := UnmarshalVector(b); err != ErrTruncated {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}
