package catalog

import (
	"math"

	"github.com/tamnd/vec/storage"
)

// lowerElem maps a catalog ElementType to the storage engine's ElemType
// (spec 02 §11, spec 04 §3.3).
func lowerElem(e ElementType) storage.ElemType {
	switch e {
	case ElemFP32:
		return storage.ElemFP32
	case ElemFP16:
		return storage.ElemFP16
	case ElemInt8:
		return storage.ElemInt8
	case ElemBinary:
		return storage.ElemBinary
	default:
		return storage.ElemFP32
	}
}

// lowerColType maps a catalog metadata Kind to the storage column type (spec 02
// §11.6). The two integer kinds share the int64 store and the two float kinds
// share the float64 store; JSON stores as text and arrays as bytes, matching the
// engine's physical cell set (spec 02 §7.12).
func lowerColType(k Kind) storage.ColType {
	switch k {
	case KindBigInt, KindInt:
		return storage.ColInt64
	case KindDouble, KindReal:
		return storage.ColFloat64
	case KindBool:
		return storage.ColBool
	case KindTimestamp:
		return storage.ColTimestamp
	case KindText, KindJSON:
		return storage.ColText
	case KindBlob, KindArray:
		return storage.ColBytes
	default:
		return storage.ColText
	}
}

// lower converts a model Value into the engine's physical storage.Value (spec 02
// §11.6). Integer and float kinds collapse to the engine's int64/float64 cells;
// JSON stores as text; arrays serialize to a length-prefixed byte form so the
// engine can hold them in a bytes column (the catalog keeps the element kind for
// rehydration). A NULL value lowers to storage.NullValue.
func (v Value) lower() storage.Value {
	switch v.kind {
	case KindNull:
		return storage.NullValue
	case KindBigInt, KindInt, KindBool, KindTimestamp:
		return storage.Value{Kind: kindToStorageKind(v.kind), I: v.i}
	case KindDouble, KindReal:
		return storage.Float(v.f)
	case KindText, KindJSON:
		return storage.Text(string(v.b))
	case KindBlob:
		return storage.BytesVal(append([]byte(nil), v.b...))
	case KindArray:
		return storage.BytesVal(encodeArray(v))
	default:
		return storage.NullValue
	}
}

func kindToStorageKind(k Kind) storage.ValueKind {
	switch k {
	case KindBigInt, KindInt:
		return storage.KindInt
	case KindBool:
		return storage.KindBool
	case KindTimestamp:
		return storage.KindTimestamp
	default:
		return storage.KindInt
	}
}

// encodeArray serializes an array value to bytes for the engine's bytes column
// (spec 02 §7.10, §7.12 array header). The form is the element kind, a varint
// element count, then each homogeneous scalar element encoded by kind. It is
// opaque to the engine and decoded by the catalog. Ordering over the encoded
// bytes is not array lexicographic, which is acceptable because arrays index
// only via GIN, not B-tree (spec 02 §7.14).
func encodeArray(v Value) []byte {
	elem, items := v.Array()
	out := make([]byte, 0, 2+len(items)*8)
	out = append(out, byte(elem))
	out = appendUvarint(out, uint64(len(items)))
	for _, it := range items {
		switch elem {
		case KindBigInt, KindInt, KindBool, KindTimestamp:
			out = appendUvarint(out, uint64(it.i))
		case KindDouble, KindReal:
			out = appendUvarint(out, math.Float64bits(it.f))
		case KindText, KindJSON, KindBlob:
			out = appendUvarint(out, uint64(len(it.b)))
			out = append(out, it.b...)
		}
	}
	return out
}

func appendUvarint(dst []byte, x uint64) []byte {
	for x >= 0x80 {
		dst = append(dst, byte(x)|0x80)
		x >>= 7
	}
	return append(dst, byte(x))
}

// nextStorageDef builds the storage.CollectionDef for this schema (spec 02 §11.2).
// It carries the primary (first) vector column's dimension, element type, and
// metric plus every metadata column as a typed storage column. Each metadata
// column is assigned a storage ColID in schema order, returned in colIDs keyed by
// column name so the live collection can address cells. Secondary vector columns
// (spec 02 §4.8) are recorded in the schema and realized by the db layer; the
// engine's single vector segment backs the primary column.
func (s *Schema) lower(collID uint64, segCapacity uint32) (storage.CollectionDef, map[string]storage.ColID) {
	prim := s.VectorColumns()[0]
	def := storage.CollectionDef{
		ID:              collID,
		Name:            s.Name,
		Dims:            prim.Dim,
		Elem:            lowerElem(prim.ElemType),
		Metric:          prim.VecMetric.distanceMetric(),
		SegmentCapacity: segCapacity,
		Int8Scale:       prim.Int8Scale,
	}
	colIDs := make(map[string]storage.ColID)
	var next storage.ColID
	for i := range s.Columns {
		c := &s.Columns[i]
		if c.Kind != ColumnMetadata {
			continue
		}
		def.Columns = append(def.Columns, storage.ColumnDef{
			ID:       next,
			Name:     c.Name,
			Type:     lowerColType(c.DataType),
			Nullable: c.Nullable,
		})
		colIDs[c.Name] = next
		next++
	}
	return def, colIDs
}
