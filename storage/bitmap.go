package storage

import "math/bits"

// PositionBitmap is a dense bitset over collection-level positions (spec 04 §10.6).
// The filter path produces one (1 = position passes the predicate) and the Index
// SPI consumes it as the pre-filter mask. It satisfies the index.Bitmap seam
// (Contains/Count) without the storage package importing index.
type PositionBitmap struct {
	words []uint64
	n     uint32 // logical position count (capacity), not the set count
}

// NewPositionBitmap allocates a bitmap sized for n positions, all clear.
func NewPositionBitmap(n uint32) *PositionBitmap {
	return &PositionBitmap{words: make([]uint64, (int(n)+63)/64), n: n}
}

// Set marks position p (no-op if out of range).
func (b *PositionBitmap) Set(p uint32) {
	if p >= b.n {
		return
	}
	b.words[p>>6] |= 1 << (p & 63)
}

// Clear unmarks position p.
func (b *PositionBitmap) Clear(p uint32) {
	if p >= b.n {
		return
	}
	b.words[p>>6] &^= 1 << (p & 63)
}

// Contains reports whether position p is set (satisfies index.Bitmap).
func (b *PositionBitmap) Contains(p uint32) bool {
	if p >= b.n {
		return false
	}
	return b.words[p>>6]&(1<<(p&63)) != 0
}

// Count returns the number of set positions (satisfies index.Bitmap).
func (b *PositionBitmap) Count() int {
	c := 0
	for _, w := range b.words {
		c += bits.OnesCount64(w)
	}
	return c
}

// Len returns the bitmap capacity (logical position count).
func (b *PositionBitmap) Len() uint32 { return b.n }

// And intersects in place with other (positions set in both survive).
func (b *PositionBitmap) And(other *PositionBitmap) {
	for i := range b.words {
		if i < len(other.words) {
			b.words[i] &= other.words[i]
		} else {
			b.words[i] = 0
		}
	}
}

// Or unions other into b in place.
func (b *PositionBitmap) Or(other *PositionBitmap) {
	for i := range b.words {
		if i < len(other.words) {
			b.words[i] |= other.words[i]
		}
	}
}

// SetAll marks every position in [0, n).
func (b *PositionBitmap) SetAll() {
	for i := range b.words {
		b.words[i] = ^uint64(0)
	}
	if tail := b.n & 63; tail != 0 && len(b.words) > 0 {
		b.words[len(b.words)-1] = (1 << tail) - 1
	}
}
