// Package vecpb is a hand-written proto3 wire codec for the VecService
// messages (spec 16 §2.2).
//
// The server speaks gRPC without importing google.golang.org/protobuf or
// grpc-go, so this package implements the proto3 binary wire format with the
// standard library only.
//
// Wire format recap (developers.google.com/protocol-buffers/docs/encoding):
//   - A tag is a varint = (field_number << 3) | wire_type.
//   - wire type 0 (varint): int32/int64/uint32/uint64/bool/enum.
//   - wire type 1 (fixed64): double, encoded little-endian.
//   - wire type 2 (length-delimited): string, bytes, embedded messages,
//     packed repeated fields.
//   - wire type 5 (fixed32): not used by these messages.
//
// proto3 semantics: scalar fields equal to their zero value are not emitted.
// Absent fields decode to the zero value. Repeated fields emit one record per
// element. The VecService proto uses no packed repeated scalars, so repeated
// uint64 ids are emitted as one varint record per element (the standard
// non-packed form, which decoders here also accept).
package vecpb

import (
	"encoding/binary"
	"errors"
	"math"
)

// Wire types.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// ErrTruncated means the buffer ended in the middle of a value.
var ErrTruncated = errors.New("vecpb: truncated message")

// ErrBadWireType means a field tag carried a wire type this codec does not
// produce and cannot skip safely.
var ErrBadWireType = errors.New("vecpb: bad wire type")

// ── Writer helpers ──────────────────────────────────────────────────────────

// appendVarint appends x as a base-128 varint.
func appendVarint(b []byte, x uint64) []byte {
	for x >= 0x80 {
		b = append(b, byte(x)|0x80)
		x >>= 7
	}
	return append(b, byte(x))
}

// appendTag appends the (field<<3)|wire tag.
func appendTag(b []byte, field int, wire uint64) []byte {
	return appendVarint(b, uint64(field)<<3|wire)
}

// appendVarintField writes a varint field if v is nonzero (proto3 default
// omission). Callers that must emit a zero (inside oneof or repeated) use the
// always variant below.
func appendVarintField(b []byte, field int, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = appendTag(b, field, wireVarint)
	return appendVarint(b, v)
}

// appendVarintFieldAlways writes a varint field even when v is zero. Used for
// oneof members and repeated elements where presence is explicit.
func appendVarintFieldAlways(b []byte, field int, v uint64) []byte {
	b = appendTag(b, field, wireVarint)
	return appendVarint(b, v)
}

// appendBoolField writes a bool field if true.
func appendBoolField(b []byte, field int, v bool) []byte {
	if !v {
		return b
	}
	b = appendTag(b, field, wireVarint)
	return appendVarint(b, 1)
}

// appendBoolFieldAlways writes a bool field even when false (oneof members).
func appendBoolFieldAlways(b []byte, field int, v bool) []byte {
	b = appendTag(b, field, wireVarint)
	var n uint64
	if v {
		n = 1
	}
	return appendVarint(b, n)
}

// appendDoubleField writes a double field if v is nonzero.
func appendDoubleField(b []byte, field int, v float64) []byte {
	if v == 0 {
		return b
	}
	return appendDoubleFieldAlways(b, field, v)
}

// appendDoubleFieldAlways writes a double field even when zero (oneof / optional).
func appendDoubleFieldAlways(b []byte, field int, v float64) []byte {
	b = appendTag(b, field, wireFixed64)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(v))
	return append(b, tmp[:]...)
}

// appendStringField writes a string field if non-empty.
func appendStringField(b []byte, field int, s string) []byte {
	if s == "" {
		return b
	}
	b = appendTag(b, field, wireBytes)
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

// appendBytesField writes a bytes field if non-empty.
func appendBytesField(b []byte, field int, p []byte) []byte {
	if len(p) == 0 {
		return b
	}
	b = appendTag(b, field, wireBytes)
	b = appendVarint(b, uint64(len(p)))
	return append(b, p...)
}

// appendBytesFieldAlways writes a bytes field even when empty (oneof members).
func appendBytesFieldAlways(b []byte, field int, p []byte) []byte {
	b = appendTag(b, field, wireBytes)
	b = appendVarint(b, uint64(len(p)))
	return append(b, p...)
}

// appendMessageField length-prefixes msg as a nested message under field. A nil
// or empty msg is still emitted as a zero-length record only when emitEmpty is
// set; otherwise an empty message is omitted.
func appendMessageField(b []byte, field int, msg []byte, emitEmpty bool) []byte {
	if len(msg) == 0 && !emitEmpty {
		return b
	}
	b = appendTag(b, field, wireBytes)
	b = appendVarint(b, uint64(len(msg)))
	return append(b, msg...)
}

// appendStringMap encodes a map<string,string> as repeated entry messages
// under field. Each entry has key=field 1, value=field 2 (both length
// delimited). Zero-value key/value are still emitted so the entry survives.
// Iteration order is the caller's map order, which the decoder reconstructs
// into an equal map.
func appendStringMap(b []byte, field int, m map[string]string) []byte {
	for k, v := range m {
		var entry []byte
		entry = appendTag(entry, 1, wireBytes)
		entry = appendVarint(entry, uint64(len(k)))
		entry = append(entry, k...)
		entry = appendTag(entry, 2, wireBytes)
		entry = appendVarint(entry, uint64(len(v)))
		entry = append(entry, v...)
		b = appendMessageField(b, field, entry, true)
	}
	return b
}

// readStringMapEntry decodes one map<string,string> entry message.
func readStringMapEntry(data []byte) (key, val string, err error) {
	r := reader{buf: data}
	for !r.done() {
		f, wire, err := r.readTag()
		if err != nil {
			return "", "", err
		}
		switch f {
		case 1:
			key, err = r.readString()
		case 2:
			val, err = r.readString()
		default:
			err = r.skip(wire)
		}
		if err != nil {
			return "", "", err
		}
	}
	return key, val, nil
}

// readValueMapEntry decodes one map<string,Value> entry message.
func readValueMapEntry(data []byte) (key string, val *Value, err error) {
	r := reader{buf: data}
	val = &Value{}
	for !r.done() {
		f, wire, err := r.readTag()
		if err != nil {
			return "", nil, err
		}
		switch f {
		case 1:
			key, err = r.readString()
		case 2:
			var p []byte
			p, err = r.readBytes()
			if err == nil {
				val, err = UnmarshalValue(p)
			}
		default:
			err = r.skip(wire)
		}
		if err != nil {
			return "", nil, err
		}
	}
	return key, val, nil
}

// appendRepeatedUint64 encodes a repeated uint64 as one varint record per
// element (the non-packed form). The VecService proto declares these as plain
// repeated, not packed.
func appendRepeatedUint64(b []byte, field int, xs []uint64) []byte {
	for _, x := range xs {
		b = appendTag(b, field, wireVarint)
		b = appendVarint(b, x)
	}
	return b
}

// ── Reader ───────────────────────────────────────────────────────────────────

// reader walks a proto3 buffer field by field.
type reader struct {
	buf []byte
	pos int
}

// done reports whether the buffer is fully consumed.
func (r *reader) done() bool { return r.pos >= len(r.buf) }

// readVarint reads a base-128 varint.
func (r *reader) readVarint() (uint64, error) {
	var x uint64
	var shift uint
	for {
		if r.pos >= len(r.buf) {
			return 0, ErrTruncated
		}
		b := r.buf[r.pos]
		r.pos++
		if shift >= 64 {
			return 0, errors.New("vecpb: varint overflow")
		}
		x |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return x, nil
		}
		shift += 7
	}
}

// readTag returns the field number and wire type of the next field.
func (r *reader) readTag() (field int, wire uint64, err error) {
	key, err := r.readVarint()
	if err != nil {
		return 0, 0, err
	}
	return int(key >> 3), key & 0x7, nil
}

// readFixed64 reads 8 little-endian bytes.
func (r *reader) readFixed64() (uint64, error) {
	if r.pos+8 > len(r.buf) {
		return 0, ErrTruncated
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return v, nil
}

// readDouble reads a fixed64 as a float64.
func (r *reader) readDouble() (float64, error) {
	v, err := r.readFixed64()
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

// readBytes reads a length-delimited byte slice. The returned slice aliases the
// source buffer; callers that retain it past the message must copy.
func (r *reader) readBytes() ([]byte, error) {
	n, err := r.readVarint()
	if err != nil {
		return nil, err
	}
	if r.pos+int(n) > len(r.buf) {
		return nil, ErrTruncated
	}
	p := r.buf[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return p, nil
}

// readBytesCopy reads a length-delimited slice and copies it.
func (r *reader) readBytesCopy() ([]byte, error) {
	p, err := r.readBytes()
	if err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, nil
	}
	out := make([]byte, len(p))
	copy(out, p)
	return out, nil
}

// readString reads a length-delimited UTF-8 string.
func (r *reader) readString() (string, error) {
	p, err := r.readBytes()
	if err != nil {
		return "", err
	}
	return string(p), nil
}

// readRepeatedUint64 appends one or more uint64 values to xs. It accepts both
// the unpacked form (wire type 0, one value) and the packed form (wire type 2,
// a varint-length block of concatenated varints), so it interoperates with any
// proto3 encoder.
func (r *reader) readRepeatedUint64(wire uint64, xs []uint64) ([]uint64, error) {
	switch wire {
	case wireVarint:
		v, err := r.readVarint()
		if err != nil {
			return nil, err
		}
		return append(xs, v), nil
	case wireBytes:
		p, err := r.readBytes()
		if err != nil {
			return nil, err
		}
		sub := reader{buf: p}
		for !sub.done() {
			v, err := sub.readVarint()
			if err != nil {
				return nil, err
			}
			xs = append(xs, v)
		}
		return xs, nil
	default:
		return nil, ErrBadWireType
	}
}

// skip advances past a field of the given wire type whose tag was already read.
// Unknown fields are skipped so forward-compatible messages decode cleanly.
func (r *reader) skip(wire uint64) error {
	switch wire {
	case wireVarint:
		_, err := r.readVarint()
		return err
	case wireFixed64:
		if r.pos+8 > len(r.buf) {
			return ErrTruncated
		}
		r.pos += 8
		return nil
	case wireBytes:
		n, err := r.readVarint()
		if err != nil {
			return err
		}
		if r.pos+int(n) > len(r.buf) {
			return ErrTruncated
		}
		r.pos += int(n)
		return nil
	case wireFixed32:
		if r.pos+4 > len(r.buf) {
			return ErrTruncated
		}
		r.pos += 4
		return nil
	default:
		return ErrBadWireType
	}
}
