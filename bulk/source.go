package bulk

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	vec "github.com/tamnd/vec"
)

// errStop is returned by a RowSource.Next when the source is exhausted. It mirrors
// io.EOF so callers can range a source with the same idiom.
var errStop = errors.New("bulk: end of source")

// Row is one decoded record from an import source: a dense vector plus the named
// fields the format carried. Binary formats that have no field names leave Fields
// nil and set AutoIndex to the 0-based row position so the driver can fall back to
// an engine-assigned id.
type Row struct {
	Vector    []float32
	Fields    map[string]any
	AutoIndex uint64
}

// RowSource streams rows from one import format. Next returns errStop when the
// source is drained. A source holds at most one decode buffer in flight, so a
// 50M-row file streams in constant memory.
type RowSource interface {
	// Next decodes and returns the next row, or errStop at end of input.
	Next() (Row, error)
	// Dim returns the vector dimension the source declares, or 0 when the
	// dimension is only known after the first row is read.
	Dim() int
	// Close releases the underlying reader.
	Close() error
}

// MetaColumnMap binds one collection metadata column to a source field (spec 17
// §2.1). SourceField names the field in the row; CollectionColumn names the column
// it lands in.
type MetaColumnMap struct {
	CollectionColumn string
	SourceField      string
}

// ImportMapping maps source fields onto the point tuple (spec 17 §2.1). The zero
// value imports the vector field named by the format default with an auto-assigned
// id and no metadata.
type ImportMapping struct {
	// IDColumn is the source field carrying the point id, or "" for an
	// engine-assigned id.
	IDColumn string
	// VectorColumn is the source field carrying the vector. For CSV it may also
	// name the first of a numbered run (v0, v1, ...); see CSVOptions.
	VectorColumn string
	// MetaColumns binds source fields to metadata columns. When empty, every
	// source field whose name matches a collection metadata column is imported.
	MetaColumns []MetaColumnMap
}

// OnError selects how a row-level import failure is handled (spec 17 §2.2, §2.7).
type OnError uint8

const (
	// WarnAndSkip records the error and continues. This is the default.
	WarnAndSkip OnError = iota
	// Abort stops the import at the first row-level error.
	Abort
)

// ImportError is one row-level import failure (spec 17 §2.7).
type ImportError struct {
	Row   int64  // 0-based source row index
	Field string // the offending field, or "" when row-wide
	Err   string // human-readable reason
	Value string // the offending value, truncated for logging
}

// String renders an ImportError as a single log line.
func (e ImportError) String() string {
	if e.Field != "" {
		return fmt.Sprintf("row %d field %q: %s (value %q)", e.Row, e.Field, e.Err, e.Value)
	}
	return fmt.Sprintf("row %d: %s", e.Row, e.Err)
}

// ImportStats is the outcome of an import (spec 17 §2.7).
type ImportStats struct {
	RowsRead     int64
	RowsImported int64
	RowsErrored  int64
	Errors       []ImportError // capped; see ImportOptions.MaxErrorsKept
}

// ImportOptions tunes an import run (spec 17 §2.7, §2.8).
type ImportOptions struct {
	// Mapping binds source fields to the point tuple.
	Mapping ImportMapping
	// OnError selects skip-vs-abort on a row-level failure.
	OnError OnError
	// BatchSize is how many points are written per UpsertBatch (default 4096).
	BatchSize int
	// AllowNonFinite keeps NaN and Inf vector elements instead of rejecting the
	// row. Off by default because non-finite elements silently corrupt recall
	// (spec 17 §2.7).
	AllowNonFinite bool
	// MaxErrorsKept caps how many ImportError records are retained in ImportStats
	// (default 100). The full count is always reported in RowsErrored.
	MaxErrorsKept int
}

func (o ImportOptions) batchSize() int {
	if o.BatchSize > 0 {
		return o.BatchSize
	}
	return 4096
}

func (o ImportOptions) maxErrorsKept() int {
	if o.MaxErrorsKept > 0 {
		return o.MaxErrorsKept
	}
	return 100
}

// columnTypes is the subset of a collection schema the driver needs to coerce a
// row: the vector column name and dimension, plus the type of every metadata
// column.
type columnTypes struct {
	vectorColumn string
	dim          int
	metric       vec.Metric
	meta         map[string]vec.ColumnType
}

// schemaForImport reads the live schema of a collection into a columnTypes.
func schemaForImport(info vec.CollectionInfo) columnTypes {
	ct := columnTypes{meta: map[string]vec.ColumnType{}}
	for _, c := range info.Columns {
		if c.Type == vec.TypeVector {
			ct.vectorColumn = c.Name
			ct.dim = c.Dim
			ct.metric = c.Metric
			continue
		}
		ct.meta[c.Name] = c.Type
	}
	return ct
}

// mapRow turns a decoded Row into a vec.Point under the mapping and schema. It
// validates the vector dimension and, unless allowed, rejects non-finite elements.
func mapRow(row Row, m ImportMapping, ct columnTypes, allowNonFinite bool) (vec.Point, error) {
	if len(row.Vector) != ct.dim {
		return vec.Point{}, fmt.Errorf("vector length %d does not match dimension %d", len(row.Vector), ct.dim)
	}
	if !allowNonFinite {
		for i, x := range row.Vector {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return vec.Point{}, fmt.Errorf("non-finite element at index %d", i)
			}
		}
	}
	p := vec.Point{
		Vectors: map[string]vec.AnyVector{
			ct.vectorColumn: {Dense: vec.FromSlice32(append([]float32(nil), row.Vector...))},
		},
	}
	// Resolve the id. An explicit mapping wins; otherwise a field literally named
	// "id" is honored so a dump or a hand-written file keeps its keys. With neither,
	// the id is left zero and the engine assigns one.
	idField := m.IDColumn
	if idField == "" {
		if _, ok := row.Fields["id"]; ok {
			idField = "id"
		}
	}
	if idField != "" {
		raw, ok := row.Fields[idField]
		if !ok {
			return vec.Point{}, fmt.Errorf("id field %q missing", idField)
		}
		id, err := toPointID(raw)
		if err != nil {
			return vec.Point{}, fmt.Errorf("id field %q: %w", idField, err)
		}
		p.ID = id
	}
	// Resolve metadata.
	binds := m.MetaColumns
	if len(binds) == 0 {
		// Implicit: every source field that names a metadata column.
		for name := range ct.meta {
			if _, ok := row.Fields[name]; ok {
				binds = append(binds, MetaColumnMap{CollectionColumn: name, SourceField: name})
			}
		}
	}
	if len(binds) > 0 {
		p.Meta = make(map[string]vec.Value, len(binds))
		for _, b := range binds {
			t, ok := ct.meta[b.CollectionColumn]
			if !ok {
				return vec.Point{}, fmt.Errorf("metadata column %q not in schema", b.CollectionColumn)
			}
			raw, present := row.Fields[b.SourceField]
			if !present || raw == nil {
				p.Meta[b.CollectionColumn] = vec.NullValue()
				continue
			}
			v, err := valueFromAny(raw, t)
			if err != nil {
				return vec.Point{}, fmt.Errorf("metadata column %q: %w", b.CollectionColumn, err)
			}
			p.Meta[b.CollectionColumn] = v
		}
	}
	return p, nil
}

// toPointID coerces a raw source value into a point id. Integers and integer-valued
// strings become integer ids; everything else becomes a byte id.
func toPointID(raw any) (vec.PointID, error) {
	switch v := raw.(type) {
	case nil:
		return vec.PointID{}, errors.New("id is null")
	case int:
		return vec.IntID(uint64(v)), nil
	case int64:
		return vec.IntID(uint64(v)), nil
	case uint64:
		return vec.IntID(v), nil
	case float64:
		if v == math.Trunc(v) && v >= 0 {
			return vec.IntID(uint64(v)), nil
		}
		return vec.BytesID([]byte(strconv.FormatFloat(v, 'g', -1, 64))), nil
	case []byte:
		return vec.BytesID(v), nil
	case string:
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return vec.IntID(n), nil
		}
		return vec.TextID(v), nil
	default:
		return vec.TextID(fmt.Sprint(v)), nil
	}
}

// valueFromAny coerces a raw source value into a typed metadata Value under the
// collection column type. Widening numeric casts are allowed; string-to-time and
// string-to-bytes follow the dump's own rendering so a dump round-trips.
func valueFromAny(raw any, t vec.ColumnType) (vec.Value, error) {
	switch t {
	case vec.TypeInt64:
		n, err := toInt64(raw)
		if err != nil {
			return vec.Value{}, err
		}
		return vec.IntValue(n), nil
	case vec.TypeFloat64:
		f, err := toFloat64(raw)
		if err != nil {
			return vec.Value{}, err
		}
		return vec.FloatValue(f), nil
	case vec.TypeBool:
		b, err := toBool(raw)
		if err != nil {
			return vec.Value{}, err
		}
		return vec.BoolValue(b), nil
	case vec.TypeText:
		return vec.TextValue(toString(raw)), nil
	case vec.TypeJSON:
		return vec.JSONValue(toString(raw)), nil
	case vec.TypeBytes:
		b, err := toBytes(raw)
		if err != nil {
			return vec.Value{}, err
		}
		return vec.BytesValue(b), nil
	case vec.TypeTimestamp:
		ts, err := toTime(raw)
		if err != nil {
			return vec.Value{}, err
		}
		return vec.TimestampValue(ts), nil
	default:
		return vec.NullValue(), nil
	}
}

func toInt64(raw any) (int64, error) {
	switch v := raw.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("cannot read %T as integer", raw)
	}
}

func toFloat64(raw any) (float64, error) {
	switch v := raw.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		return 0, fmt.Errorf("cannot read %T as float", raw)
	}
}

func toBool(raw any) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		return strconv.ParseBool(strings.TrimSpace(v))
	case float64:
		return v != 0, nil
	case int64:
		return v != 0, nil
	default:
		return false, fmt.Errorf("cannot read %T as bool", raw)
	}
}

func toString(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

func toBytes(raw any) ([]byte, error) {
	switch v := raw.(type) {
	case []byte:
		return v, nil
	case string:
		// The dump renders bytes columns as hex; accept hex first, then raw.
		if b, err := hex.DecodeString(v); err == nil {
			return b, nil
		}
		return []byte(v), nil
	default:
		return nil, fmt.Errorf("cannot read %T as bytes", raw)
	}
}

func toTime(raw any) (time.Time, error) {
	switch v := raw.(type) {
	case time.Time:
		return v, nil
	case string:
		return time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
	case float64:
		return time.Unix(0, int64(v)).UTC(), nil
	case int64:
		return time.Unix(0, v).UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("cannot read %T as timestamp", raw)
	}
}

// parseVectorField coerces a source field into a dense float32 vector. It accepts a
// float slice, a slice of JSON numbers, or a bracketed decimal string.
func parseVectorField(raw any) ([]float32, error) {
	switch v := raw.(type) {
	case []float32:
		return v, nil
	case []float64:
		out := make([]float32, len(v))
		for i, x := range v {
			out[i] = float32(x)
		}
		return out, nil
	case []any:
		out := make([]float32, len(v))
		for i, x := range v {
			f, err := toFloat64(x)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			out[i] = float32(f)
		}
		return out, nil
	case string:
		return parseVectorString(v)
	default:
		return nil, fmt.Errorf("cannot read %T as a vector", raw)
	}
}

// parseVectorString parses a bracketed decimal list such as "[0.1, 0.2, 0.3]".
func parseVectorString(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		out[i] = float32(f)
	}
	return out, nil
}
