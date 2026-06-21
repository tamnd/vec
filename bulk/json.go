package bulk

import (
	"encoding/json"
	"fmt"
	"io"
)

// JSONOptions tunes the JSON and JSONL readers (spec 17 §2.3).
type JSONOptions struct {
	// VectorField names the JSON field holding the vector array (default
	// "embedding", then "vector").
	VectorField string
}

func (o JSONOptions) vectorField() string {
	if o.VectorField != "" {
		return o.VectorField
	}
	return ""
}

// jsonSource streams rows from JSONL (one object per line) or from a single
// top-level JSON array. encoding/json.Decoder drives both so neither buffers the
// whole file (spec 17 §2.3).
type jsonSource struct {
	dec      *json.Decoder
	vecField string
	array    bool // true once we have consumed the opening '[' of an array
	rowIdx   uint64
	started  bool
}

// NewJSONLSource builds a RowSource over newline-delimited JSON objects.
func NewJSONLSource(r io.Reader, opts JSONOptions) (RowSource, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	return &jsonSource{dec: dec, vecField: opts.vectorField()}, nil
}

// NewJSONSource builds a RowSource over a single top-level array of objects. It
// consumes the opening bracket on the first Next.
func NewJSONSource(r io.Reader, opts JSONOptions) (RowSource, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	return &jsonSource{dec: dec, vecField: opts.vectorField(), array: true}, nil
}

func (s *jsonSource) Dim() int { return 0 }

func (s *jsonSource) Next() (Row, error) {
	if s.array && !s.started {
		// Consume the opening '['.
		tok, err := s.dec.Token()
		if err != nil {
			return Row{}, err
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return Row{}, fmt.Errorf("bulk: JSON source: expected an array, got %v", tok)
		}
		s.started = true
	}
	if s.array && !s.dec.More() {
		return Row{}, errStop
	}
	var obj map[string]any
	if err := s.dec.Decode(&obj); err != nil {
		if err == io.EOF {
			return Row{}, errStop
		}
		return Row{}, fmt.Errorf("bulk: decode row %d: %w", s.rowIdx, err)
	}
	row := Row{AutoIndex: s.rowIdx, Fields: make(map[string]any, len(obj))}
	s.rowIdx++

	vecField := s.vecField
	if vecField == "" {
		// Discover the vector field once, by name, then by first array value.
		for _, name := range []string{"embedding", "vector"} {
			if _, ok := obj[name]; ok {
				vecField = name
				break
			}
		}
		if vecField == "" {
			for name, v := range obj {
				if _, ok := v.([]any); ok {
					vecField = name
					break
				}
			}
		}
		if vecField == "" {
			return Row{}, fmt.Errorf("bulk: row %d: no vector field found", row.AutoIndex)
		}
		s.vecField = vecField
	}

	rawVec, ok := obj[vecField]
	if !ok {
		return Row{}, fmt.Errorf("bulk: row %d: vector field %q missing", row.AutoIndex, vecField)
	}
	vecf, err := jsonVector(rawVec)
	if err != nil {
		return Row{}, fmt.Errorf("bulk: row %d vector: %w", row.AutoIndex, err)
	}
	row.Vector = vecf

	for name, v := range obj {
		row.Fields[name] = normalizeJSON(v)
	}
	return row, nil
}

func (s *jsonSource) Close() error { return nil }

// jsonVector reads a JSON array (decoded with UseNumber) into a float32 slice.
func jsonVector(raw any) ([]float32, error) {
	arr, ok := raw.([]any)
	if !ok {
		// A bracketed string vector is also accepted for symmetry with CSV.
		if str, ok := raw.(string); ok {
			return parseVectorString(str)
		}
		return nil, fmt.Errorf("vector is %T, want array", raw)
	}
	out := make([]float32, len(arr))
	for i, x := range arr {
		switch n := x.(type) {
		case json.Number:
			f, err := n.Float64()
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			out[i] = float32(f)
		case float64:
			out[i] = float32(n)
		default:
			return nil, fmt.Errorf("element %d is %T, want number", i, x)
		}
	}
	return out, nil
}

// normalizeJSON folds json.Number into int64 or float64 so downstream coercion
// sees plain Go scalars rather than the decoder's number wrapper.
func normalizeJSON(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}
