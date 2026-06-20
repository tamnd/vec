package vec

import (
	"fmt"
	"strconv"
	"time"

	"github.com/tamnd/vec/catalog"
	"github.com/tamnd/vec/storage"
)

// ColumnType is the declared type of a collection column (spec 14 §4.2). A vector
// column carries a dimension and a metric; the remaining types are scalar
// metadata columns.
type ColumnType uint8

const (
	TypeVector ColumnType = iota // a fixed-length dense vector column
	TypeInt64
	TypeFloat64
	TypeBool
	TypeText
	TypeBytes
	TypeJSON
	TypeTimestamp
	typeNull // internal: the type of a NULL value
)

// String renders a ColumnType as its canonical type name.
func (t ColumnType) String() string {
	switch t {
	case TypeVector:
		return "vector"
	case TypeInt64:
		return "int64"
	case TypeFloat64:
		return "float64"
	case TypeBool:
		return "bool"
	case TypeText:
		return "text"
	case TypeBytes:
		return "bytes"
	case TypeJSON:
		return "json"
	case TypeTimestamp:
		return "timestamp"
	default:
		return "null"
	}
}

// Metric is the distance metric bound to a vector column (spec 14 §4.3). It is
// fixed at collection creation and governs both index construction and query
// distance computation.
type Metric uint8

const (
	MetricL2 Metric = iota
	MetricCosine
	MetricDot
	MetricHamming
	MetricJaccard
)

// catalogMetric maps a public Metric to the catalog's metric enum.
func (m Metric) catalogMetric() catalog.Metric {
	switch m {
	case MetricCosine:
		return catalog.MetricCosine
	case MetricDot:
		return catalog.MetricInnerProduct
	case MetricHamming:
		return catalog.MetricHamming
	case MetricJaccard:
		return catalog.MetricJaccard
	default:
		return catalog.MetricL2
	}
}

// IndexType selects the ANN index implementation for a vector column (spec 14
// §6.2). IndexFlat is the exact brute-force oracle; the rest are approximate.
type IndexType uint8

const (
	IndexFlat IndexType = iota
	IndexHNSW
	IndexIVFFlat
	IndexIVFPQ
	IndexDiskANN
)

// String renders an IndexType as its canonical name.
func (t IndexType) String() string {
	switch t {
	case IndexHNSW:
		return "hnsw"
	case IndexIVFFlat:
		return "ivfflat"
	case IndexIVFPQ:
		return "ivfpq"
	case IndexDiskANN:
		return "diskann"
	default:
		return "flat"
	}
}

// Value is a typed metadata value (spec 14 §5). It is a discriminated union over
// the scalar, text, bytes, and timestamp types a metadata column may hold. The
// zero Value is NULL. Construct values with the *Value functions and read them
// back with the typed accessors.
type Value struct {
	cv catalog.Value
}

// NullValue is the absent value (spec 14 §5.2).
func NullValue() Value { return Value{cv: catalog.Null} }

// IntValue builds a 64-bit integer value.
func IntValue(i int64) Value { return Value{cv: catalog.BigInt(i)} }

// FloatValue builds a 64-bit float value.
func FloatValue(f float64) Value { return Value{cv: catalog.Double(f)} }

// BoolValue builds a boolean value.
func BoolValue(b bool) Value { return Value{cv: catalog.Bool(b)} }

// TextValue builds a UTF-8 string value.
func TextValue(s string) Value { return Value{cv: catalog.Text(s)} }

// BytesValue builds a raw byte value.
func BytesValue(b []byte) Value { return Value{cv: catalog.Blob(b)} }

// JSONValue builds a JSON value stored as UTF-8.
func JSONValue(s string) Value { return Value{cv: catalog.JSON(s)} }

// TimestampValue builds a timestamp value.
func TimestampValue(t time.Time) Value { return Value{cv: catalog.Timestamp(t)} }

// IsNull reports whether the value is NULL.
func (v Value) IsNull() bool { return v.cv.IsNull() }

// Type returns the value's column type.
func (v Value) Type() ColumnType {
	switch v.cv.Kind() {
	case catalog.KindBigInt, catalog.KindInt:
		return TypeInt64
	case catalog.KindDouble, catalog.KindReal:
		return TypeFloat64
	case catalog.KindBool:
		return TypeBool
	case catalog.KindText:
		return TypeText
	case catalog.KindBlob:
		return TypeBytes
	case catalog.KindJSON:
		return TypeJSON
	case catalog.KindTimestamp:
		return TypeTimestamp
	default:
		return typeNull
	}
}

// Int returns the integer payload (zero for other kinds).
func (v Value) Int() int64 { return v.cv.BigInt() }

// Float returns the float payload (zero for other kinds).
func (v Value) Float() float64 { return v.cv.Double() }

// Bool returns the boolean payload.
func (v Value) Bool() bool { return v.cv.Bool() }

// Text returns the string payload.
func (v Value) Text() string { return v.cv.Text() }

// Bytes returns the byte payload.
func (v Value) Bytes() []byte { return v.cv.Blob() }

// Time returns the timestamp payload as a UTC time.Time.
func (v Value) Time() time.Time { return v.cv.Time() }

// String renders the value for diagnostics and the %s verb.
func (v Value) String() string {
	switch v.Type() {
	case TypeInt64:
		return strconv.FormatInt(v.Int(), 10)
	case TypeFloat64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case TypeBool:
		return strconv.FormatBool(v.Bool())
	case TypeText, TypeJSON:
		return v.cv.Text()
	case TypeBytes:
		return fmt.Sprintf("%x", v.Bytes())
	case TypeTimestamp:
		return v.Time().Format(time.RFC3339Nano)
	default:
		return "NULL"
	}
}

// catalogValue returns the underlying catalog value for lowering on the write path.
func (v Value) catalogValue() catalog.Value { return v.cv }

// valueFromStorage converts an engine cell into a public Value for query results.
func valueFromStorage(sv storage.Value) Value {
	switch sv.Kind {
	case storage.KindInt:
		return IntValue(sv.I)
	case storage.KindFloat:
		return FloatValue(sv.F)
	case storage.KindBool:
		return BoolValue(sv.B)
	case storage.KindText:
		return TextValue(sv.S)
	case storage.KindBytes:
		return BytesValue(sv.Bytes)
	case storage.KindTimestamp:
		return TimestampValue(time.Unix(0, sv.I).UTC())
	default:
		return NullValue()
	}
}
