package catalog

import (
	"fmt"
	"math"
)

// maxValueBytes is the TEXT/BLOB/JSON length limit (spec 02 §13.11).
const maxValueBytes = math.MaxInt32

// validateVector checks a vector value for a vector column on the write path
// (spec 02 §4.5, §4.6): dimension, NaN, Inf, and per-element range. The enforced
// order matches spec 02 §10.5 (dimension and element-type checks). A nil vector
// is the caller's NULL signal and is handled before this is called.
func validateVector(col *ColumnDef, vec []float32) error {
	if uint32(len(vec)) != col.Dim {
		return fmt.Errorf("%w: column %q expects %d, got %d", ErrDimMismatch, col.Name, col.Dim, len(vec))
	}
	for i, x := range vec {
		switch {
		case math.IsNaN(float64(x)):
			return fmt.Errorf("%w: column %q element %d", ErrNaNInVector, col.Name, i)
		case math.IsInf(float64(x), 0):
			return fmt.Errorf("%w: column %q element %d", ErrInfInVector, col.Name, i)
		}
		switch col.ElemType {
		case ElemInt8:
			if x < -128 || x > 127 {
				return fmt.Errorf("%w: column %q int8 element %d = %g", ErrValueOutOfRange, col.Name, i, x)
			}
		case ElemBinary:
			if x != 0 && x != 1 {
				return fmt.Errorf("%w: column %q binary element %d = %g", ErrValueOutOfRange, col.Name, i, x)
			}
		}
	}
	return nil
}

// kindAssignable reports whether a value kind may be stored in a column of the
// declared metadata kind under schema-fixed mode (spec 02 §13.7). The integer
// kinds are mutually assignable and so are the float kinds, matching the
// numeric-promotion rule of spec 02 §7.13; JSON and arrays must match exactly.
func kindAssignable(declared, got Kind) bool {
	if declared == got {
		return true
	}
	switch declared {
	case KindBigInt, KindInt:
		return got == KindBigInt || got == KindInt
	case KindDouble, KindReal:
		return got == KindDouble || got == KindReal || got == KindBigInt || got == KindInt
	default:
		return false
	}
}

// validateMeta validates one metadata cell against its column under schema-fixed
// mode (spec 02 §10.5): NOT NULL, type assignability, array element kind, and the
// value-size limit. Schema-optional mode skips type checks (spec 02 §13.7); the
// caller passes optional=true for that mode.
func validateMeta(col *ColumnDef, v Value, optional bool) error {
	if v.IsNull() {
		if !col.Nullable {
			return fmt.Errorf("%w: column %q", ErrNullViolation, col.Name)
		}
		return nil
	}
	if !optional && !kindAssignable(col.DataType, v.Kind()) {
		return fmt.Errorf("%w: column %q is %s, got %s", ErrTypeMismatch, col.Name, col.DataType, v.Kind())
	}
	switch v.Kind() {
	case KindText, KindBlob, KindJSON:
		if len(v.b) > maxValueBytes {
			return fmt.Errorf("%w: column %q", ErrValueTooLarge, col.Name)
		}
	case KindArray:
		if !optional && col.DataType == KindArray {
			elem, items := v.Array()
			if elem != col.ArrayElem {
				return fmt.Errorf("%w: column %q array element is %s, got %s", ErrTypeMismatch, col.Name, col.ArrayElem, elem)
			}
			for _, it := range items {
				if it.IsNull() {
					return fmt.Errorf("%w: column %q array element is NULL", ErrNullViolation, col.Name)
				}
			}
		}
	}
	return nil
}

// resolveMeta builds the full metadata row for an insert, supplying defaults for
// columns the caller omitted and validating every cell (spec 02 §9.3 lazy
// defaults, §9.6 default expressions, §10.5 enforcement order). now is the
// transaction start time used for DEFAULT NOW() (spec 02 §9.6). The returned map
// is keyed by column name and holds a value for every metadata column in
// schema-fixed mode.
func (s *Schema) resolveMeta(supplied map[string]Value, now Value) (map[string]Value, error) {
	out := make(map[string]Value, len(s.Columns))
	optional := s.Mode == SchemaOptional
	for i := range s.Columns {
		col := &s.Columns[i]
		if col.Kind != ColumnMetadata {
			continue
		}
		v, ok := supplied[col.Name]
		if !ok {
			switch {
			case col.DefaultNow:
				v = now
			case col.Default != nil:
				v = *col.Default
			case optional:
				continue // schema-optional: omitted column is simply absent
			default:
				v = Null
			}
		}
		if err := validateMeta(col, v, optional); err != nil {
			return nil, err
		}
		out[col.Name] = v
	}
	// In schema-optional mode, carry through any column not in the schema; the
	// collection auto-registers it on first write (spec 02 §2.4). The catalog
	// records the inferred kind out of band; here it is passed through.
	if optional {
		for name, v := range supplied {
			if s.Column(name) == nil {
				out[name] = v
			}
		}
	}
	return out, nil
}
