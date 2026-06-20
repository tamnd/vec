package format

import "encoding/binary"

// Varint encoding (spec 03 §13). vec uses Go's standard LEB128 unsigned varint
// for record fields and lengths, and zigzag for signed values. This matches the
// kv record encoding so the shared B-tree cell format is byte-identical
// (spec 03 §5.5, [kv 05](../2059/05-btree-core.md)). MaxVarintLen64 bytes is the
// worst case for a 64-bit value.
const MaxVarintLen64 = binary.MaxVarintLen64

// PutUvarint writes x into buf in LEB128 form and returns the byte count. buf
// must have room for MaxVarintLen64 bytes in the worst case.
func PutUvarint(buf []byte, x uint64) int {
	return binary.PutUvarint(buf, x)
}

// Uvarint reads a LEB128 unsigned varint from buf, returning the value and the
// number of bytes consumed. A non-positive count signals an error (overflow or
// truncation), matching binary.Uvarint.
func Uvarint(buf []byte) (uint64, int) {
	return binary.Uvarint(buf)
}

// PutVarint writes a signed value using zigzag+LEB128 and returns the byte count.
func PutVarint(buf []byte, x int64) int {
	return binary.PutVarint(buf, x)
}

// Varint reads a zigzag+LEB128 signed value, returning the value and byte count.
func Varint(buf []byte) (int64, int) {
	return binary.Varint(buf)
}

// UvarintLen returns the number of bytes PutUvarint would write for x, without
// writing. Used to size records before encoding.
func UvarintLen(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}

// AppendUvarint appends the LEB128 form of x to dst and returns the extended
// slice. Convenient for building variable-length records.
func AppendUvarint(dst []byte, x uint64) []byte {
	return binary.AppendUvarint(dst, x)
}

// AppendVarint appends the zigzag+LEB128 form of x to dst.
func AppendVarint(dst []byte, x int64) []byte {
	return binary.AppendVarint(dst, x)
}

// AppendBytes appends a length-prefixed byte string (uvarint length then the
// bytes) to dst. This is the canonical variable-length field encoding for
// records and catalog entries (spec 03 §13).
func AppendBytes(dst, b []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(b)))
	return append(dst, b...)
}

// TakeBytes reads a length-prefixed byte string written by AppendBytes from buf,
// returning the bytes (a subslice of buf, not a copy) and the remaining buffer.
// It returns ErrShortBuffer on truncation.
func TakeBytes(buf []byte) (b, rest []byte, err error) {
	n, k := binary.Uvarint(buf)
	if k <= 0 {
		return nil, buf, ErrShortBuffer
	}
	buf = buf[k:]
	if uint64(len(buf)) < n {
		return nil, buf, ErrShortBuffer
	}
	return buf[:n], buf[n:], nil
}
