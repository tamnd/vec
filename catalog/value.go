// Package catalog implements the vec data model and schema authority (spec 02):
// the value type system, point identity, distance-metric binding, collection
// schemas, the three system collections, schema definition and evolution rules,
// constraints, and the write-path validation every insert and upsert must pass.
//
// The catalog sits above the storage engine ([04], the storage package) and
// below the query layer ([10]-[13]). It owns the logical schema (column names,
// types, dimensions, metrics, nullability, defaults) that the spec calls the
// authoritative source of truth (spec 02 §2.3), and it lowers each schema into a
// storage.CollectionDef so the engine can lay out segments (spec 02 §11). The
// model Value here is the application-facing value (spec 02 §16.1); the engine's
// storage.Value is the physical cell, and Value.lower bridges the two.
package catalog

import (
	"math"
	"time"
)

// Kind identifies the concrete type inside a Value (spec 02 §16.1). It is the
// metadata value type system of spec 02 §7: the scalar, temporal, text, binary,
// JSON, and array types a metadata column may hold.
type Kind uint8

const (
	KindNull      Kind = 0  // the absent value (spec 02 §7.11)
	KindBigInt    Kind = 1  // int64 (spec 02 §7.3)
	KindInt       Kind = 2  // int32, stored in the int64 slot (spec 02 §7.3)
	KindDouble    Kind = 3  // float64 (spec 02 §7.4)
	KindReal      Kind = 4  // float32, stored in the float64 slot (spec 02 §7.4)
	KindBool      Kind = 5  // boolean (spec 02 §7.5)
	KindText      Kind = 6  // UTF-8 string (spec 02 §7.6)
	KindTimestamp Kind = 7  // microseconds since the Unix epoch, UTC (spec 02 §7.7)
	KindBlob      Kind = 8  // raw bytes (spec 02 §7.8)
	KindJSON      Kind = 9  // JSON value stored as UTF-8 (spec 02 §7.9)
	KindArray     Kind = 10 // homogeneous array of a scalar kind (spec 02 §7.10)
)

// String renders a Kind as its canonical SQL type name (spec 02 §7.2).
func (k Kind) String() string {
	switch k {
	case KindNull:
		return "null"
	case KindBigInt:
		return "bigint"
	case KindInt:
		return "int"
	case KindDouble:
		return "double"
	case KindReal:
		return "real"
	case KindBool:
		return "bool"
	case KindText:
		return "text"
	case KindTimestamp:
		return "timestamp"
	case KindBlob:
		return "blob"
	case KindJSON:
		return "json"
	case KindArray:
		return "array"
	default:
		return "kind?"
	}
}

// isNumeric reports whether the kind participates in cross-kind numeric
// comparison (spec 02 §7.13): the two integer kinds and the two float kinds
// compare against each other by numeric value.
func (k Kind) isNumeric() bool {
	switch k {
	case KindBigInt, KindInt, KindDouble, KindReal:
		return true
	default:
		return false
	}
}

// Value is a discriminated union for a metadata column value (spec 02 §16.1).
// Scalar kinds store in the i or f slot and never allocate; Text, Blob, JSON,
// and Array carry a payload. The zero Value is NULL.
type Value struct {
	kind Kind
	i    int64   // KindBigInt, KindInt, KindBool (0/1), KindTimestamp (micros)
	f    float64 // KindDouble, KindReal
	b    []byte  // KindText, KindBlob, KindJSON (all UTF-8 or raw bytes)
	arr  []Value // KindArray elements
	elem Kind    // KindArray element kind; KindNull otherwise
}

// Null is the absent value (spec 02 §7.11).
var Null = Value{kind: KindNull}

// BigInt builds a 64-bit signed integer value (spec 02 §7.3).
func BigInt(n int64) Value { return Value{kind: KindBigInt, i: n} }

// Int builds a 32-bit signed integer value (spec 02 §7.3).
func Int(n int32) Value { return Value{kind: KindInt, i: int64(n)} }

// Double builds a 64-bit float value (spec 02 §7.4).
func Double(f float64) Value { return Value{kind: KindDouble, f: f} }

// Real builds a 32-bit float value, widened to float64 for storage (spec 02 §7.4).
func Real(f float32) Value { return Value{kind: KindReal, f: float64(f)} }

// Bool builds a boolean value (spec 02 §7.5).
func Bool(t bool) Value {
	v := Value{kind: KindBool}
	if t {
		v.i = 1
	}
	return v
}

// Text builds a UTF-8 string value (spec 02 §7.6).
func Text(s string) Value { return Value{kind: KindText, b: []byte(s)} }

// Timestamp builds a timestamp value stored as UTC epoch microseconds (spec 02 §7.7).
func Timestamp(t time.Time) Value { return Value{kind: KindTimestamp, i: t.UnixMicro()} }

// Blob builds a raw byte value (spec 02 §7.8).
func Blob(p []byte) Value { return Value{kind: KindBlob, b: append([]byte(nil), p...)} }

// JSON builds a JSON value stored as UTF-8 (spec 02 §7.9). The caller is
// responsible for supplying valid JSON; the binder validates it (spec 02 §7.9).
func JSON(s string) Value { return Value{kind: KindJSON, b: []byte(s)} }

// Array builds a homogeneous array value over elem-kinded scalars (spec 02 §7.10).
func Array(elem Kind, items []Value) Value {
	return Value{kind: KindArray, elem: elem, arr: append([]Value(nil), items...)}
}

// Kind returns the value's kind.
func (v Value) Kind() Kind { return v.kind }

// IsNull reports whether the value is NULL (spec 02 §7.11).
func (v Value) IsNull() bool { return v.kind == KindNull }

// BigInt returns the int64 payload (KindBigInt or KindInt).
func (v Value) BigInt() int64 { return v.i }

// Int returns the int32 payload (KindInt).
func (v Value) Int() int32 { return int32(v.i) }

// Double returns the float64 payload (KindDouble or KindReal).
func (v Value) Double() float64 { return v.f }

// Real returns the float32 payload (KindReal).
func (v Value) Real() float32 { return float32(v.f) }

// Bool returns the boolean payload (KindBool).
func (v Value) Bool() bool { return v.i != 0 }

// Text returns the string payload (KindText or KindJSON).
func (v Value) Text() string { return string(v.b) }

// Time returns the timestamp as a UTC time.Time (KindTimestamp).
func (v Value) Time() time.Time { return time.UnixMicro(v.i).UTC() }

// Blob returns the byte payload (KindBlob).
func (v Value) Blob() []byte { return v.b }

// Array returns the element kind and the array elements (KindArray).
func (v Value) Array() (Kind, []Value) { return v.elem, v.arr }

// asFloat returns the value as a float64 for numeric comparison and reports
// whether the kind is numeric (spec 02 §7.13).
func (v Value) asFloat() (float64, bool) {
	switch v.kind {
	case KindBigInt, KindInt:
		return float64(v.i), true
	case KindDouble, KindReal:
		return v.f, true
	default:
		return 0, false
	}
}

// Equal evaluates v = other under SQL three-valued logic (spec 02 §7.11, §7.13).
// It returns (result, known): known is false when either side is NULL or when a
// float NaN is involved, in which case the comparison is UNKNOWN. Numeric kinds
// compare across the integer/float boundary by value; JSON is never equal via =
// at the column level (spec 02 §7.13).
func (v Value) Equal(other Value) (result bool, known bool) {
	if v.IsNull() || other.IsNull() {
		return false, false
	}
	if v.kind == KindJSON || other.kind == KindJSON {
		return false, false
	}
	if v.kind.isNumeric() && other.kind.isNumeric() {
		a, _ := v.asFloat()
		b, _ := other.asFloat()
		if math.IsNaN(a) || math.IsNaN(b) {
			return false, false // NaN is not equal to anything, including itself
		}
		return a == b, true
	}
	if v.kind != other.kind {
		return false, true
	}
	switch v.kind {
	case KindBool, KindTimestamp:
		return v.i == other.i, true
	case KindText:
		return string(v.b) == string(other.b), true
	case KindBlob:
		return bytesEqual(v.b, other.b), true
	case KindArray:
		if v.elem != other.elem || len(v.arr) != len(other.arr) {
			return false, true
		}
		for i := range v.arr {
			eq, ok := v.arr[i].Equal(other.arr[i])
			if !ok || !eq {
				return false, ok || (i == len(v.arr))
			}
		}
		return true, true
	default:
		return false, true
	}
}

// Less evaluates v < other under three-valued logic (spec 02 §7.13). It returns
// (result, known); known is false when either side is NULL or a NaN is involved.
func (v Value) Less(other Value) (result bool, known bool) {
	if v.IsNull() || other.IsNull() {
		return false, false
	}
	if v.kind.isNumeric() && other.kind.isNumeric() {
		a, _ := v.asFloat()
		b, _ := other.asFloat()
		if math.IsNaN(a) || math.IsNaN(b) {
			return false, false
		}
		return a < b, true
	}
	if v.kind != other.kind {
		return false, true
	}
	switch v.kind {
	case KindBool:
		return v.i < other.i, true // FALSE < TRUE (spec 02 §7.13)
	case KindTimestamp:
		return v.i < other.i, true
	case KindText, KindJSON:
		return string(v.b) < string(other.b), true // code-point lexicographic
	case KindBlob:
		return bytesLess(v.b, other.b), true // unsigned byte lexicographic
	case KindArray:
		return arrayLess(v.arr, other.arr), true
	default:
		return false, true
	}
}

// Order returns a total-order comparison of two non-null values for ORDER BY
// (spec 02 §7.13): -1, 0, or 1. NaN sorts greatest among floats; NULL handling
// (NULLS LAST/FIRST) is the caller's, since it is a clause-level choice.
func (v Value) Order(other Value) int {
	if v.kind.isNumeric() && other.kind.isNumeric() {
		a, _ := v.asFloat()
		b, _ := other.asFloat()
		an, bn := math.IsNaN(a), math.IsNaN(b)
		switch {
		case an && bn:
			return 0
		case an:
			return 1 // NaN is the greatest value (spec 02 §7.4)
		case bn:
			return -1
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
	switch v.kind {
	case KindBool, KindTimestamp:
		switch {
		case v.i < other.i:
			return -1
		case v.i > other.i:
			return 1
		default:
			return 0
		}
	case KindText, KindJSON:
		return cmpString(string(v.b), string(other.b))
	case KindBlob:
		return cmpBytes(v.b, other.b)
	case KindArray:
		return cmpArray(v.arr, other.arr)
	default:
		return 0
	}
}

// Clone returns a deep copy so a stored value never aliases the caller's buffer
// (spec 02 §13.12 in-place overwrite path).
func (v Value) Clone() Value {
	switch v.kind {
	case KindText, KindBlob, KindJSON:
		return Value{kind: v.kind, b: append([]byte(nil), v.b...)}
	case KindArray:
		out := make([]Value, len(v.arr))
		for i := range v.arr {
			out[i] = v.arr[i].Clone()
		}
		return Value{kind: KindArray, elem: v.elem, arr: out}
	default:
		return v
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func bytesLess(a, b []byte) bool { return cmpBytes(a, b) < 0 }

func cmpBytes(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func arrayLess(a, b []Value) bool { return cmpArray(a, b) < 0 }

func cmpArray(a, b []Value) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if c := a[i].Order(b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
