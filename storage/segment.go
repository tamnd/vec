package storage

import "github.com/tamnd/vector/mvcc"

// segment is one fixed-stride columnar vector segment (spec 04 §3). It holds the
// single copy of capacity slots' worth of vectors in one contiguous byte run, plus
// the per-slot tombstone bitmap, write-version array, and backward map. An open
// segment appends at nextPos; a sealed segment is immutable except for tombstones
// (spec 04 §3.7). This build keeps the byte run in memory; the on-disk pager-backed
// run (spec 04 §3.8) is a later slice with identical addressing.
type segment struct {
	seqNum   uint32
	elem     ElemType
	dims     uint32
	stride   uint32
	capacity uint32
	sealed   bool
	nextPos  uint32 // next free slot; equals live+tombstone once sealed

	codec    vecCodec
	data     []byte           // capacity * stride bytes, the single vector copy
	tomb     []uint64         // tombstone bitmap, one bit per slot
	version  []mvcc.CommitSeq // write_version per slot (spec 04 §8.6)
	backward []PointID        // slot -> point id (spec 04 §6.3)

	liveCount uint32
	deadCount uint32
}

// newSegment allocates an empty open segment of the given capacity (spec 04 §9.2).
func newSegment(seqNum uint32, elem ElemType, dims, stride, capacity uint32, codec vecCodec) *segment {
	return &segment{
		seqNum:   seqNum,
		elem:     elem,
		dims:     dims,
		stride:   stride,
		capacity: capacity,
		codec:    codec,
		data:     make([]byte, int(capacity)*int(stride)),
		tomb:     make([]uint64, (int(capacity)+63)/64),
		version:  make([]mvcc.CommitSeq, capacity),
		backward: make([]PointID, capacity),
	}
}

// SeqNum returns the segment sequence number.
func (s *segment) SeqNum() uint32 { return s.seqNum }

// ElemType returns the stored element type.
func (s *segment) ElemType() ElemType { return s.elem }

// Dims returns the vector dimensionality.
func (s *segment) Dims() uint32 { return s.dims }

// Stride returns the per-slot byte stride.
func (s *segment) Stride() uint32 { return s.stride }

// Capacity returns the slot capacity.
func (s *segment) Capacity() uint32 { return s.capacity }

// LiveCount returns the number of live (non-tombstoned) slots.
func (s *segment) LiveCount() uint32 { return s.liveCount }

// TombstoneCount returns the number of tombstoned slots.
func (s *segment) TombstoneCount() uint32 { return s.deadCount }

// IsSealed reports whether the segment is sealed (immutable).
func (s *segment) IsSealed() bool { return s.sealed }

// full reports whether the open segment has no free slot left.
func (s *segment) full() bool { return s.nextPos >= s.capacity }

// append writes a vector into the next free slot and records its id and version
// (spec 04 §3.6, §8.1). It returns the slot position. The caller guarantees the
// segment is open and not full.
func (s *segment) append(id PointID, vec []float32, ver mvcc.CommitSeq) uint32 {
	pos := s.nextPos
	off := int(pos) * int(s.stride)
	s.codec.encode(vec, s.data[off:off+s.codec.rawBytes()])
	s.backward[pos] = id
	s.version[pos] = ver
	s.nextPos++
	s.liveCount++
	return pos
}

// FetchVector decodes the vector at slotPos into buf, which must hold dims floats
// (spec 04 §15.2). int8/fp16/binary slots are dequantized here (spec 04 §18.5).
func (s *segment) FetchVector(slotPos uint32, buf []float32) error {
	if slotPos >= s.nextPos {
		return ErrPositionOutOfRange
	}
	off := int(slotPos) * int(s.stride)
	s.codec.decode(s.data[off:off+s.codec.rawBytes()], buf)
	return nil
}

// IsTombstoned reports whether slotPos is tombstoned (spec 04 §15.2).
func (s *segment) IsTombstoned(slotPos uint32) bool {
	if slotPos >= s.capacity {
		return true
	}
	return s.tomb[slotPos>>6]&(1<<(slotPos&63)) != 0
}

// tombstone marks slotPos dead (spec 04 §6.4). Idempotent.
func (s *segment) tombstone(slotPos uint32) {
	if slotPos >= s.capacity || s.IsTombstoned(slotPos) {
		return
	}
	s.tomb[slotPos>>6] |= 1 << (slotPos & 63)
	if s.liveCount > 0 {
		s.liveCount--
	}
	s.deadCount++
}

// VersionAt returns the write_version of the point at slotPos (spec 04 §15.2).
func (s *segment) VersionAt(slotPos uint32) uint64 {
	if slotPos >= s.capacity {
		return 0
	}
	return uint64(s.version[slotPos])
}

// BackwardMapLookup returns the point id stored at slotPos (spec 04 §15.2).
func (s *segment) BackwardMapLookup(slotPos uint32) (PointID, error) {
	if slotPos >= s.nextPos {
		return 0, ErrPositionOutOfRange
	}
	return s.backward[slotPos], nil
}

// Scan iterates every live slot in ascending order (spec 04 §15.2). The vec passed
// to cb is a freshly decoded buffer the callback may retain.
func (s *segment) Scan(cb func(slotPos uint32, vec []float32) bool) error {
	for p := uint32(0); p < s.nextPos; p++ {
		if s.IsTombstoned(p) {
			continue
		}
		buf := make([]float32, s.dims)
		off := int(p) * int(s.stride)
		s.codec.decode(s.data[off:off+s.codec.rawBytes()], buf)
		if !cb(p, buf) {
			break
		}
	}
	return nil
}

// Pages returns the placeholder page range for this segment (spec 04 §15.2). In
// the in-memory build no pager pages are allocated, so this reports a zero range;
// the pager-backed slice fills it in.
func (s *segment) Pages() (firstPgno uint32, count uint32) { return 0, 0 }

// visibleLive reports whether slotPos is live and visible in snap (spec 04 §8.6,
// §13.2): not tombstoned and its write version committed at or before the snapshot.
func (s *segment) visibleLive(slotPos uint32, snap Snapshot) bool {
	if s.IsTombstoned(slotPos) {
		return false
	}
	if snap == nil {
		return true
	}
	return snap.IsVisible(s.version[slotPos], 0)
}
