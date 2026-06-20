package storage

// This file states the storage SPI as Go interfaces (spec 04 §15) and asserts that
// the concrete engine and segment types satisfy them. The id-map and metadata
// store of spec 04 §15.3 and §15.4 are realized by the unexported idMap and
// metadataStore types, which the engine owns directly rather than through an
// abstract interface, so only the two caller-facing seams are declared here.

// StorageEngine is the central object the executor and Index SPI call (spec 04
// §15.1). One StorageEngine exists per open database.
type StorageEngine interface {
	Fetch(collID uint64, P uint32, proj []ColID, snap Snapshot) (PointRecord, error)
	FetchBatch(collID uint64, positions []uint32, proj []ColID, snap Snapshot) ([]PointRecord, error)
	FetchVector(collID uint64, P uint32, buf []float32) error
	ScanVectors(collID uint64, snap Snapshot, cb func(pos uint32, vec []float32) bool) error

	Insert(txn Txn, collID uint64, id PointID, vec []float32, meta MetaRow) (uint32, error)
	InsertBatch(txn Txn, collID uint64, points []Point) ([]uint32, error)
	Delete(txn Txn, collID uint64, id PointID) error
	Upsert(txn Txn, collID uint64, id PointID, vec []float32, meta MetaRow) (uint32, bool, error)
	UpdateMeta(txn Txn, collID uint64, id PointID, meta MetaRow) error

	MetadataFilter(collID uint64, pred Predicate, snap Snapshot) (*PositionBitmap, error)

	LookupID(collID uint64, id PointID) (uint32, error)
	LookupPos(collID uint64, P uint32) (PointID, error)

	CollectionStats(collID uint64) (CollectionStats, error)
	ColumnStats(collID uint64, colID ColID) (ColumnStats, error)
	Analyze(collID uint64) error

	Compact(collID uint64, lo, hi uint32) error
	SealSegment(collID uint64) error
	WarmCache(collID uint64) error
	Close() error
}

// VectorSegment represents one sealed or open segment of vector data (spec 04
// §15.2). Callers obtain segments through the engine; they do not create them.
type VectorSegment interface {
	SeqNum() uint32
	ElemType() ElemType
	Dims() uint32
	Stride() uint32
	Capacity() uint32
	LiveCount() uint32
	TombstoneCount() uint32
	IsSealed() bool

	FetchVector(slotPos uint32, buf []float32) error
	IsTombstoned(slotPos uint32) bool
	VersionAt(slotPos uint32) uint64
	BackwardMapLookup(slotPos uint32) (PointID, error)
	Scan(cb func(slotPos uint32, vec []float32) bool) error
	Pages() (firstPgno uint32, count uint32)
}

var (
	_ StorageEngine = (*Engine)(nil)
	_ VectorSegment = (*segment)(nil)
)
