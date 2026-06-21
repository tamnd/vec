package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/vec"
)

// parseMetric maps a metric name to the library metric (spec 16 §3).
func parseMetric(name string) (vec.Metric, error) {
	switch strings.ToLower(name) {
	case "", "l2", "euclidean":
		return vec.MetricL2, nil
	case "cosine", "cos":
		return vec.MetricCosine, nil
	case "dot", "ip", "inner_product":
		return vec.MetricDot, nil
	case "hamming":
		return vec.MetricHamming, nil
	case "jaccard":
		return vec.MetricJaccard, nil
	default:
		return vec.MetricL2, fmt.Errorf("unknown metric %q", name)
	}
}

// metricName renders a library metric as its wire name.
func metricName(m vec.Metric) string {
	switch m {
	case vec.MetricCosine:
		return "cosine"
	case vec.MetricDot:
		return "dot"
	case vec.MetricHamming:
		return "hamming"
	case vec.MetricJaccard:
		return "jaccard"
	default:
		return "l2"
	}
}

// parseIndexType maps an index name to the library index type. An unknown name
// falls back to flat, the exact brute-force index.
func parseIndexType(name string) vec.IndexType {
	switch strings.ToLower(name) {
	case "hnsw":
		return vec.IndexHNSW
	case "ivfflat", "ivf":
		return vec.IndexIVFFlat
	case "ivfpq":
		return vec.IndexIVFPQ
	case "diskann":
		return vec.IndexDiskANN
	default:
		return vec.IndexFlat
	}
}

// parseColumnType maps a metadata column type name to the library type.
func parseColumnType(name string) (vec.ColumnType, error) {
	switch strings.ToLower(name) {
	case "int", "int64", "bigint", "integer":
		return vec.TypeInt64, nil
	case "float", "float64", "double", "real":
		return vec.TypeFloat64, nil
	case "bool", "boolean":
		return vec.TypeBool, nil
	case "text", "string", "varchar":
		return vec.TypeText, nil
	case "bytes", "blob", "bytea":
		return vec.TypeBytes, nil
	case "json", "jsonb":
		return vec.TypeJSON, nil
	case "timestamp", "timestamptz", "datetime":
		return vec.TypeTimestamp, nil
	default:
		return vec.TypeText, fmt.Errorf("unknown column type %q", name)
	}
}

// indexParams converts the string map from the wire into the library's typed
// index parameter map, parsing integer and float values where they parse.
func indexParams(in map[string]string) vec.IndexParams {
	if len(in) == 0 {
		return nil
	}
	out := make(vec.IndexParams, len(in))
	for k, v := range in {
		if n, err := strconv.Atoi(v); err == nil {
			out[k] = n
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out[k] = f
			continue
		}
		out[k] = v
	}
	return out
}

// toMeta converts a decoded JSON or proto payload into library metadata values.
func toMeta(payload map[string]any) (map[string]vec.Value, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	out := make(map[string]vec.Value, len(payload))
	for k, v := range payload {
		val, err := toValue(v)
		if err != nil {
			return nil, fmt.Errorf("payload field %q: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}

// toValue converts one decoded scalar into a library Value.
func toValue(v any) (vec.Value, error) {
	switch x := v.(type) {
	case nil:
		return vec.NullValue(), nil
	case bool:
		return vec.BoolValue(x), nil
	case string:
		return vec.TextValue(x), nil
	case int:
		return vec.IntValue(int64(x)), nil
	case int64:
		return vec.IntValue(x), nil
	case float64:
		// JSON numbers decode as float64; keep whole numbers as integers.
		if x == float64(int64(x)) {
			return vec.IntValue(int64(x)), nil
		}
		return vec.FloatValue(x), nil
	case float32:
		return vec.FloatValue(float64(x)), nil
	case time.Time:
		return vec.TimestampValue(x), nil
	case []byte:
		return vec.BytesValue(x), nil
	default:
		return vec.NullValue(), fmt.Errorf("unsupported value type %T", v)
	}
}

// fromMeta converts library metadata values into a plain map for the wire.
func fromMeta(meta map[string]vec.Value) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for k, v := range meta {
		out[k] = fromValue(v)
	}
	return out
}

// fromValue converts one library Value into a plain Go scalar.
func fromValue(v vec.Value) any {
	switch v.Type() {
	case vec.TypeInt64:
		return v.Int()
	case vec.TypeFloat64:
		return v.Float()
	case vec.TypeBool:
		return v.Bool()
	case vec.TypeText, vec.TypeJSON:
		return v.Text()
	case vec.TypeBytes:
		return v.Bytes()
	case vec.TypeTimestamp:
		return v.Time().Format(time.RFC3339Nano)
	default:
		return nil
	}
}
