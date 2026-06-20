package storage

import (
	"github.com/tamnd/vector/distance"
	"github.com/tamnd/vector/mvcc"
)

// PointID is the stable, application-supplied identity of a point (spec 04 §6.1).
// It never changes and is never reused after delete. The id-map translates it to
// a dense engine position, which an ANN graph can store in a fixed 4 bytes per
// edge (spec 04 §20.4).
type PointID = mvcc.PointID

// Snapshot is an MVCC read snapshot (spec 04 §13.2, spec 06). A read sees only
// versions committed at or before its watermark; in-flight writes are invisible.
type Snapshot = *mvcc.Snapshot

// ColID identifies a metadata column within a collection (spec 04 §5).
type ColID uint32

// ElemType is the stored element representation of a vector segment (spec 04 §3.3).
type ElemType uint8

const (
	ElemFP32   ElemType = 0 // 4 bytes/elem, 32-byte stride alignment
	ElemFP16   ElemType = 1 // 2 bytes/elem, 16-byte alignment
	ElemInt8   ElemType = 2 // 1 byte/elem, 16-byte alignment, dequantized on read
	ElemBinary ElemType = 3 // 1 bit/elem, 8-byte alignment, Hamming distance
)

// elemAlign returns the SIMD stride alignment for an element type (spec 04 §18).
func (e ElemType) elemAlign() int {
	switch e {
	case ElemFP32:
		return 32
	case ElemFP16, ElemInt8:
		return 16
	case ElemBinary:
		return 8
	default:
		return 32
	}
}

// rawBytes returns the unaligned byte size of dims elements (spec 04 §3.3).
func (e ElemType) rawBytes(dims uint32) int {
	switch e {
	case ElemFP32:
		return int(dims) * 4
	case ElemFP16:
		return int(dims) * 2
	case ElemInt8:
		return int(dims)
	case ElemBinary:
		return (int(dims) + 7) / 8
	default:
		return int(dims) * 4
	}
}

// computeStride returns the per-slot byte stride: the raw element bytes rounded up
// to the element type's SIMD alignment (spec 04 §3.3). base + pos*stride is then
// always aligned when base is, so SIMD loads never straddle the alignment boundary.
func computeStride(elem ElemType, dims uint32) uint32 {
	raw := elem.rawBytes(dims)
	align := elem.elemAlign()
	return uint32((raw + align - 1) &^ (align - 1))
}

// ColType is the logical type of a metadata column (spec 04 §5, spec 02 §4).
type ColType uint8

const (
	ColInt64     ColType = 0
	ColFloat64   ColType = 1
	ColBool      ColType = 2
	ColTimestamp ColType = 3 // unix nanoseconds, stored as int64
	ColText      ColType = 4
	ColBytes     ColType = 5
)

// ColumnDef declares one metadata column of a collection (spec 04 §22.1).
type ColumnDef struct {
	ID       ColID
	Name     string
	Type     ColType
	Nullable bool
}

// CollectionDef declares the physical parameters of a collection (spec 04 §22.1).
// SegmentCapacity is the per-segment point capacity; 0 derives it from the default
// 256 MB segment target divided by the stride (spec 04 §3.5).
type CollectionDef struct {
	ID              uint64
	Name            string
	Dims            uint32
	Elem            ElemType
	Metric          distance.Metric
	Columns         []ColumnDef
	SegmentCapacity uint32
	// Int8Scale is the symmetric scale used to dequantize ElemInt8 segments on read
	// (spec 04 §18.5). Ignored for other element types; 0 defaults to 1.0.
	Int8Scale float32
}

// MetaRow is the metadata payload of a point, keyed by column id (spec 04 §15.1).
// A missing column is treated as NULL. Columns not in the schema are rejected.
type MetaRow map[ColID]Value

// Point is one element of a bulk insert (spec 04 §15.1).
type Point struct {
	ID   PointID
	Vec  []float32
	Meta MetaRow
}

// PointRecord is the resolved, snapshot-visible record returned by Fetch
// (spec 04 §7.1): the id, the full-precision vector, and the projected metadata.
type PointRecord struct {
	Pos  uint32
	ID   PointID
	Vec  []float32
	Meta MetaRow
}

// Repoint describes one position mapping change during compaction (spec 04 §15.6).
// The slice handed to ApplyRepoint and to the Index SPI is sorted by OldPos
// ascending so position-sorted index structures can binary-search it.
type Repoint struct {
	OldPos  uint32
	NewPos  uint32
	PointID PointID
}

// IdMapStats reports id-map size (spec 04 §15.3).
type IdMapStats struct {
	LiveEntries       int64
	TombstonedEntries int64
}

// CollectionStats are the aggregate statistics the planner consumes (spec 04 §12.2).
type CollectionStats struct {
	TotalPoints        uint64
	LivePoints         uint64
	TombstoneCount     uint64
	SegmentCount       int
	Dims               uint32
	Stride             uint32
	StalenessScore     float64 // 0 fresh, 1 very stale (spec 04 §21.2)
	WritesSinceAnalyze uint64
}

// ColumnStats are per-column statistics for selectivity estimation (spec 04 §12.3).
type ColumnStats struct {
	ColID         ColID
	NullFraction  float64
	DistinctCount uint64
	Min           Value
	Max           Value
	Histogram     []HistogramBucket
}

// HistogramBucket is one equi-depth bucket of a numeric column histogram
// (spec 04 §21.2): values in [Lo, Hi] cover roughly Count points.
type HistogramBucket struct {
	Lo    float64
	Hi    float64
	Count uint64
}

// ZoneMapStats summarize zone map effectiveness for a column (spec 04 §12.4).
type ZoneMapStats struct {
	BlockCount   int
	AvgZoneRange float64
	Sorted       bool
}
