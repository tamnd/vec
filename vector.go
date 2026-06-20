package vec

import "math"

// Vector is a dense float32 vector (spec 14 §3.1). It is the in-memory form a
// caller passes to Upsert and Query; the engine stores one copy per point and the
// indexes reference positions, never copies.
type Vector []float32

// NewVector returns a zeroed vector of the given dimension.
func NewVector(dim int) Vector { return make(Vector, dim) }

// FromSlice32 wraps a float32 slice as a Vector without copying.
func FromSlice32(s []float32) Vector { return Vector(s) }

// FromSlice64 builds a Vector from a float64 slice, narrowing each element.
func FromSlice64(s []float64) Vector {
	v := make(Vector, len(s))
	for i, x := range s {
		v[i] = float32(x)
	}
	return v
}

// ToSlice32 returns the vector as a float32 slice without copying.
func (v Vector) ToSlice32() []float32 { return []float32(v) }

// ToSlice64 returns the vector widened to a float64 slice.
func (v Vector) ToSlice64() []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}

// L2Norm returns the Euclidean norm of the vector.
func (v Vector) L2Norm() float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return float32(math.Sqrt(sum))
}

// Normalize returns a unit-length copy of the vector; a zero vector is returned
// unchanged.
func (v Vector) Normalize() Vector {
	n := v.L2Norm()
	if n == 0 {
		return append(Vector(nil), v...)
	}
	out := make(Vector, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

// Dot returns the dot product of v and o; mismatched lengths return 0.
func (v Vector) Dot(o Vector) float32 {
	if len(v) != len(o) {
		return 0
	}
	var sum float64
	for i := range v {
		sum += float64(v[i]) * float64(o[i])
	}
	return float32(sum)
}

// Cosine returns the cosine similarity of v and o in [-1, 1].
func (v Vector) Cosine(o Vector) float32 {
	dn := v.L2Norm() * o.L2Norm()
	if dn == 0 {
		return 0
	}
	return v.Dot(o) / dn
}

// HalfPrecision rounds each element to the nearest IEEE half-precision value,
// returning a float32 vector. It models the precision loss of an fp16 column so a
// caller can preview quantization effects.
func (v Vector) HalfPrecision() Vector {
	out := make(Vector, len(v))
	for i, x := range v {
		out[i] = math.Float32frombits(halfToFloatBits(floatToHalfBits(x)))
	}
	return out
}

// SparseVector is a sparse vector as parallel index/value arrays (spec 14 §3.2).
// Indices must be strictly increasing.
type SparseVector struct {
	Indices []uint32
	Values  []float32
	Dim     uint32
}

// MultiVector is a set of dense vectors for late-interaction retrieval (spec 14
// §3.3), such as ColBERT token embeddings.
type MultiVector []Vector

// AnyVector is the vector payload for one column of a point (spec 14 §3.4).
// Exactly one of Dense, Sparse, or Multi is set, matching the column kind.
type AnyVector struct {
	Dense  Vector
	Sparse *SparseVector
	Multi  MultiVector
}

// PointID is the identity of a point (spec 14 §3.5). A collection uses one form,
// fixed at creation: an integer N, or a byte/text key B with IsBytes set.
type PointID struct {
	N       uint64
	B       []byte
	IsBytes bool
}

// IntID builds an integer point id.
func IntID(n uint64) PointID { return PointID{N: n} }

// TextID builds a text point id.
func TextID(s string) PointID { return PointID{B: []byte(s), IsBytes: true} }

// BytesID builds a bytes point id.
func BytesID(b []byte) PointID { return PointID{B: append([]byte(nil), b...), IsBytes: true} }

// Point is a row to upsert: its identity, its vectors keyed by column name, and
// its metadata keyed by column name (spec 14 §3.6). For a single-vector
// collection the Vectors map has one entry.
type Point struct {
	ID      PointID
	Vectors map[string]AnyVector
	Meta    map[string]Value
}

// ColumnDef declares one column of a collection schema (spec 14 §4.2).
type ColumnDef struct {
	Name    string
	Type    ColumnType
	Dim     int    // vector columns only: element count
	Metric  Metric // vector columns only: distance metric
	NotNull bool
	Default *Value
}

// CollectionSchema describes a collection at creation (spec 14 §4.1).
type CollectionSchema struct {
	Name    string
	Columns []ColumnDef
	Comment string
}

// CollectionInfo is a snapshot of a collection's identity and size (spec 14 §4.6).
type CollectionInfo struct {
	Name       string
	Columns    []ColumnDef
	PointCount int64
}

// IndexInfo describes one ANN index on a collection (spec 14 §6.4).
type IndexInfo struct {
	Name   string
	Column string
	Type   IndexType
	Params IndexParams
}

// IndexParams holds index-build tuning knobs by name (spec 14 §6.3), such as
// "m" and "ef_construction" for HNSW or "nlist" and "nprobe" for IVF.
type IndexParams map[string]any

// IndexStatsDetail reports live statistics for one index (spec 14 §6.5).
type IndexStatsDetail struct {
	Name           string
	Type           IndexType
	NodeCount      int64
	TombstoneCount int64
	MemoryBytes    int64
}

// IndexBuildStats is the progress callback payload during an index build
// (spec 14 §6.6, §7.7).
type IndexBuildStats struct {
	Phase       string
	PointsDone  int64
	PointsTotal int64
}

// floatToHalfBits converts a float32 to IEEE half-precision bits (round-to-nearest).
func floatToHalfBits(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16((b >> 16) & 0x8000)
	exp := int32((b>>23)&0xff) - 127 + 15
	mant := b & 0x7fffff
	switch {
	case exp <= 0:
		return sign
	case exp >= 0x1f:
		return sign | 0x7c00
	default:
		return sign | uint16(exp<<10) | uint16(mant>>13)
	}
}

// halfToFloatBits expands IEEE half-precision bits back to float32 bits.
func halfToFloatBits(h uint16) uint32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	mant := uint32(h & 0x3ff)
	switch exp {
	case 0:
		if mant == 0 {
			return sign
		}
		return sign | (mant << 13) | (uint32(127-15+1) << 23)
	case 0x1f:
		return sign | 0x7f800000 | (mant << 13)
	default:
		return sign | ((exp + (127 - 15)) << 23) | (mant << 13)
	}
}
