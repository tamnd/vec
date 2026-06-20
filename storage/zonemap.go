package storage

// zoneBlockSize is the number of consecutive positions covered by one zone map
// block (spec 04 §5.4). Filter pushdown skips a whole block when its [Min, Max]
// cannot satisfy the predicate.
const zoneBlockSize = 1024

// ZoneBlock summarizes one run of positions in a column segment (spec 04 §5.4).
// Min and Max bound the live, non-null values; a block with Count == 0 holds no
// summarizable value and is never skippable. Invariant I-5 (spec 04 §25.1): the
// zone MAY be wider than the true range but MUST NOT be narrower.
type ZoneBlock struct {
	Min       Value
	Max       Value
	NullCount uint32
	Count     uint32 // non-null live values folded into Min/Max
}

// ZoneMap is the per-column-segment array of zone blocks (spec 04 §5.4).
type ZoneMap struct {
	Blocks []ZoneBlock
}

// fold widens the block covering slotPos to include v (spec 04 §5.8 insert-time
// maintenance). A NULL only bumps the null counter. The block array grows as
// positions are appended.
func (z *ZoneMap) fold(slotPos uint32, v Value) {
	bi := int(slotPos) / zoneBlockSize
	for len(z.Blocks) <= bi {
		z.Blocks = append(z.Blocks, ZoneBlock{})
	}
	blk := &z.Blocks[bi]
	if v.IsNull() {
		blk.NullCount++
		return
	}
	if blk.Count == 0 {
		blk.Min = v
		blk.Max = v
	} else {
		if v.less(blk.Min) {
			blk.Min = v
		}
		if blk.Max.less(v) {
			blk.Max = v
		}
	}
	blk.Count++
}

// blockFor returns the zone block index for a slot position.
func blockFor(slotPos uint32) int { return int(slotPos) / zoneBlockSize }

// canSkip reports whether the predicate term (col op lit) provably matches no
// live value in the block, so the block can be skipped during a filter scan
// (spec 04 §5.4). It is conservative: when in doubt it returns false (scan).
func (b ZoneBlock) canSkip(op CmpOp, lit Value) bool {
	if b.Count == 0 {
		return false // no summarizable value; cannot prove a skip
	}
	switch op {
	case OpEq:
		// Skippable only if lit lies strictly outside [Min, Max].
		return lit.less(b.Min) || b.Max.less(lit)
	case OpLt:
		// All values >= lit -> no value < lit. Skip when Min >= lit.
		return !b.Min.less(lit)
	case OpLe:
		// Skip when Min > lit.
		return lit.less(b.Min)
	case OpGt:
		// Skip when Max <= lit.
		return !lit.less(b.Max)
	case OpGe:
		// Skip when Max < lit.
		return b.Max.less(lit)
	default:
		return false
	}
}
