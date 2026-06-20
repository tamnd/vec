package storage

import "math"

// ValueKind tags a metadata Value (spec 04 §5.2).
type ValueKind uint8

const (
	KindNull      ValueKind = 0
	KindInt       ValueKind = 1
	KindFloat     ValueKind = 2
	KindBool      ValueKind = 3
	KindText      ValueKind = 4
	KindBytes     ValueKind = 5
	KindTimestamp ValueKind = 6
)

// Value is a single metadata cell: a small tagged union covering the scalar and
// variable-length column types (spec 04 §5.2, §5.5). NullValue is the zero value.
type Value struct {
	Kind  ValueKind
	I     int64
	F     float64
	B     bool
	S     string
	Bytes []byte
}

// NullValue is the absent / NULL cell (spec 04 §5.3).
var NullValue = Value{Kind: KindNull}

// IsNull reports whether the value is NULL.
func (v Value) IsNull() bool { return v.Kind == KindNull }

// Int builds an int64 value.
func Int(i int64) Value { return Value{Kind: KindInt, I: i} }

// Float builds a float64 value.
func Float(f float64) Value { return Value{Kind: KindFloat, F: f} }

// Bool builds a bool value.
func Bool(b bool) Value { return Value{Kind: KindBool, B: b} }

// Text builds a text value.
func Text(s string) Value { return Value{Kind: KindText, S: s} }

// BytesVal builds a bytes value.
func BytesVal(b []byte) Value { return Value{Kind: KindBytes, Bytes: b} }

// Timestamp builds a timestamp value (unix nanoseconds).
func Timestamp(ns int64) Value { return Value{Kind: KindTimestamp, I: ns} }

// asFloat coerces an ordered scalar to a float64 for histograms and zone map
// width math (spec 04 §12.3). Non-ordered kinds return (0, false).
func (v Value) asFloat() (float64, bool) {
	switch v.Kind {
	case KindInt, KindTimestamp:
		return float64(v.I), true
	case KindFloat:
		return v.F, true
	case KindBool:
		if v.B {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// order returns -1, 0, or 1 comparing v to o within the same comparable kind.
// Text and Bytes compare lexicographically. Mixed numeric kinds compare by their
// float projection. Comparing across non-comparable kinds returns (0, false).
func (v Value) order(o Value) (int, bool) {
	if v.Kind == KindNull || o.Kind == KindNull {
		return 0, false
	}
	if (v.Kind == KindText || v.Kind == KindBytes) != (o.Kind == KindText || o.Kind == KindBytes) {
		return 0, false
	}
	switch v.Kind {
	case KindText:
		return cmpString(v.S, o.S), true
	case KindBytes:
		return cmpString(string(v.Bytes), string(o.Bytes)), true
	}
	a, ok1 := v.asFloat()
	b, ok2 := o.asFloat()
	if !ok1 || !ok2 {
		return 0, false
	}
	switch {
	case a < b:
		return -1, true
	case a > b:
		return 1, true
	default:
		return 0, true
	}
}

// equal reports value equality across the comparable kinds.
func (v Value) equal(o Value) bool {
	if v.Kind == KindNull || o.Kind == KindNull {
		return v.Kind == o.Kind
	}
	switch v.Kind {
	case KindText:
		return o.Kind == KindText && v.S == o.S
	case KindBytes:
		return o.Kind == KindBytes && string(v.Bytes) == string(o.Bytes)
	case KindBool:
		return o.Kind == KindBool && v.B == o.B
	}
	a, ok1 := v.asFloat()
	b, ok2 := o.asFloat()
	return ok1 && ok2 && a == b
}

// less reports v < o using order; incomparable pairs are reported false.
func (v Value) less(o Value) bool {
	c, ok := v.order(o)
	return ok && c < 0
}

// clone deep-copies the variable-length payload so a stored Value never aliases
// the caller's buffer (spec 04 §8.7 in-place overwrite must own its bytes).
func (v Value) clone() Value {
	if v.Kind == KindBytes && v.Bytes != nil {
		b := make([]byte, len(v.Bytes))
		copy(b, v.Bytes)
		v.Bytes = b
	}
	return v
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

// floatKey returns a sortable float64 for any ordered numeric value, used by the
// histogram builder; NaN inputs are mapped to +Inf so they sort last.
func floatKey(v Value) float64 {
	f, ok := v.asFloat()
	if !ok || math.IsNaN(f) {
		return math.Inf(1)
	}
	return f
}
