package catalog

import (
	"fmt"
	"strings"
)

// ColumnKind distinguishes vector columns from metadata columns (spec 02 §16.3).
type ColumnKind uint8

const (
	ColumnVector   ColumnKind = 1 // a fixed-length vector column (spec 02 §4)
	ColumnMetadata ColumnKind = 2 // a typed scalar/composite column (spec 02 §7)
)

// IDKind is the point id form a collection uses for all its points (spec 02 §3.2).
// A collection uses exactly one form, fixed at creation.
type IDKind uint8

const (
	IDBigInt IDKind = 1 // uint64, the compact form; the only form auto-assignment uses
	IDText   IDKind = 2 // UTF-8 string up to 255 bytes
	IDBlob   IDKind = 3 // raw bytes up to 255 bytes
)

// String renders an IDKind for the catalog.
func (k IDKind) String() string {
	switch k {
	case IDBigInt:
		return "bigint"
	case IDText:
		return "text"
	case IDBlob:
		return "blob"
	default:
		return "id?"
	}
}

// SchemaMode is the schema enforcement mode (spec 02 §2.4).
type SchemaMode uint8

const (
	SchemaFixed    SchemaMode = 1 // declared types enforced on every write (default)
	SchemaOptional SchemaMode = 2 // metadata types inferred, may be heterogeneous
)

// String renders a SchemaMode for the catalog (vec_collections.schema_mode).
func (m SchemaMode) String() string {
	if m == SchemaOptional {
		return "optional"
	}
	return "fixed"
}

// ColumnDef describes one column of a collection schema (spec 02 §16.3). The
// vector fields are set for ColumnVector, the metadata fields for ColumnMetadata.
type ColumnDef struct {
	Name    string
	Kind    ColumnKind
	Ordinal int // position in the schema, 0-based (spec 02 §2.3)

	// Vector-column fields (Kind == ColumnVector):
	Dim       uint32      // element count, 1..65535 (spec 02 §4.2)
	ElemType  ElementType // fp32/fp16/int8/binary
	VecMetric Metric      // bound metric, defaulted per element type if unset
	Normalize bool        // WITH NORMALIZATION ON (spec 02 §4.7)
	Int8Scale float32     // symmetric dequantization scale for int8 (spec 02 §4.3)

	// Metadata-column fields (Kind == ColumnMetadata):
	DataType   Kind   // the scalar/composite kind of the column
	ArrayElem  Kind   // element kind for KindArray columns, KindNull otherwise
	Nullable   bool   // whether the column accepts NULL (spec 02 §9.7)
	Default    *Value // default value expression, nil if none (spec 02 §9.6)
	DefaultNow bool   // DEFAULT NOW() for a timestamp column (spec 02 §9.6)
}

// IsVector reports whether the column is a vector column.
func (c *ColumnDef) IsVector() bool { return c.Kind == ColumnVector }

// Schema describes a collection's schema (spec 02 §16.3). It is the
// authoritative source of truth for column names, types, dimensions, metrics,
// nullability, and defaults (spec 02 §2.3).
type Schema struct {
	Name          string
	IDName        string // the primary-key column name (spec 02 §2.3, default "id")
	IDKind        IDKind
	AutoIncrement bool // id is auto-assigned from a sequence (spec 02 §3.5)
	Mode          SchemaMode
	Columns       []ColumnDef
}

// VectorColumns returns the vector columns in schema order (spec 02 §4.8).
func (s *Schema) VectorColumns() []ColumnDef {
	var out []ColumnDef
	for _, c := range s.Columns {
		if c.Kind == ColumnVector {
			out = append(out, c)
		}
	}
	return out
}

// MetadataColumns returns the metadata columns in schema order (spec 02 §7).
func (s *Schema) MetadataColumns() []ColumnDef {
	var out []ColumnDef
	for _, c := range s.Columns {
		if c.Kind == ColumnMetadata {
			out = append(out, c)
		}
	}
	return out
}

// Column returns the column with the given name, or nil if absent.
func (s *Schema) Column(name string) *ColumnDef {
	for i := range s.Columns {
		if s.Columns[i].Name == name {
			return &s.Columns[i]
		}
	}
	return nil
}

// maxCollectionNameBytes is the collection-name length limit (spec 02 §2.2).
const maxCollectionNameBytes = 255

// maxDim is the vector dimension limit, a u16 value (spec 02 §4.2).
const maxDim = 65535

// maxMetadataColumns caps the metadata column count (spec 02 §9.3).
const maxMetadataColumns = 1024

// validateName checks a user collection name against spec 02 §2.2: non-empty, at
// most 255 bytes, and not using the reserved vec_ prefix.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: collection name is empty", ErrInvalidSchema)
	}
	if len(name) > maxCollectionNameBytes {
		return fmt.Errorf("%w: collection name exceeds %d bytes", ErrInvalidSchema, maxCollectionNameBytes)
	}
	if strings.HasPrefix(name, "vec_") {
		return ErrReservedName
	}
	return nil
}

// normalize fills defaulted fields (id name, metric, schema mode, ordinals) and
// validates the schema against the structural rules of spec 02 §9.1: at least
// one vector column, unique column names, valid dimensions, a metric compatible
// with each vector column's element type, and a metadata column count within the
// limit. It mutates the schema in place and returns the first violation.
func (s *Schema) normalize() error {
	if err := validateName(s.Name); err != nil {
		return err
	}
	if s.IDName == "" {
		s.IDName = "id"
	}
	if s.Mode == 0 {
		s.Mode = SchemaFixed
	}
	if s.AutoIncrement && s.IDKind != IDBigInt {
		return fmt.Errorf("%w: AUTOINCREMENT requires a BIGINT primary key", ErrInvalidSchema)
	}

	seen := make(map[string]struct{}, len(s.Columns))
	vecCount, metaCount := 0, 0
	for i := range s.Columns {
		c := &s.Columns[i]
		if c.Name == "" {
			return fmt.Errorf("%w: column %d has no name", ErrInvalidSchema, i)
		}
		if c.Name == s.IDName || c.Name == "id" || c.Name == "point_id" {
			return fmt.Errorf("%w: column name %q collides with the primary key", ErrInvalidSchema, c.Name)
		}
		if _, dup := seen[c.Name]; dup {
			return fmt.Errorf("%w: duplicate column name %q", ErrInvalidSchema, c.Name)
		}
		seen[c.Name] = struct{}{}
		c.Ordinal = i

		switch c.Kind {
		case ColumnVector:
			vecCount++
			if c.Dim == 0 || c.Dim > maxDim {
				return fmt.Errorf("%w: vector column %q dimension %d out of range 1..%d", ErrInvalidSchema, c.Name, c.Dim, maxDim)
			}
			if c.ElemType == 0 {
				c.ElemType = ElemFP32
			}
			if c.VecMetric == 0 {
				c.VecMetric = DefaultMetric(c.ElemType)
			}
			if !MetricSupported(c.VecMetric, c.ElemType) {
				return fmt.Errorf("%w: metric %s not valid for %s column %q", ErrMetricUnsupported, c.VecMetric, c.ElemType, c.Name)
			}
			if c.ElemType == ElemInt8 && c.Int8Scale == 0 {
				c.Int8Scale = 1.0
			}
		case ColumnMetadata:
			metaCount++
			if c.DataType == KindNull {
				return fmt.Errorf("%w: metadata column %q has no type", ErrInvalidSchema, c.Name)
			}
			if c.DataType == KindArray && c.ArrayElem == KindNull {
				return fmt.Errorf("%w: array column %q has no element type", ErrInvalidSchema, c.Name)
			}
		default:
			return fmt.Errorf("%w: column %q has unknown kind", ErrInvalidSchema, c.Name)
		}
	}
	if vecCount == 0 {
		return fmt.Errorf("%w: schema has no vector column", ErrInvalidSchema)
	}
	if metaCount > maxMetadataColumns {
		return fmt.Errorf("%w: %d metadata columns exceeds limit %d", ErrInvalidSchema, metaCount, maxMetadataColumns)
	}
	return nil
}
