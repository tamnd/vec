package mvcc

import (
	"math"
	"sync"
)

// WatermarkOracle tracks the read point of every live snapshot and reports the
// minimum, which is the single number that governs version reclamation: any
// version whose CommitSeq is strictly below the watermark is invisible to all
// live transactions and may be folded into the base and dropped (spec 06 §2.5).
// It is the same structure vec shares with the kv and gr siblings.
type WatermarkOracle struct {
	mu      sync.Mutex
	live    map[TxnID]CommitSeq // txn_id -> read_seq for each live snapshot
	current *Clock              // fallback when no readers are live
}

// NewWatermarkOracle returns an oracle that falls back to clk.Current when no
// snapshot is live (all versions are then reclaimable, spec 06 §2.5).
func NewWatermarkOracle(clk *Clock) *WatermarkOracle {
	return &WatermarkOracle{live: make(map[TxnID]CommitSeq), current: clk}
}

// Register records a live snapshot's read point (spec 06 §2.5). Called when a
// transaction takes its snapshot.
func (w *WatermarkOracle) Register(txnID TxnID, readSeq CommitSeq) {
	w.mu.Lock()
	w.live[txnID] = readSeq
	w.mu.Unlock()
}

// Deregister drops a snapshot when its transaction ends, possibly advancing the
// watermark (spec 06 §2.5).
func (w *WatermarkOracle) Deregister(txnID TxnID) {
	w.mu.Lock()
	delete(w.live, txnID)
	w.mu.Unlock()
}

// Watermark returns the lowest read point among live snapshots, or the current
// commit sequence if no reader is live (spec 06 §2.5).
func (w *WatermarkOracle) Watermark() CommitSeq {
	w.mu.Lock()
	defer w.mu.Unlock()
	min := CommitSeq(math.MaxUint64)
	for _, r := range w.live {
		if r < min {
			min = r
		}
	}
	if min == CommitSeq(math.MaxUint64) {
		return w.current.Current()
	}
	return min
}
