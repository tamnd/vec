package mvcc

import (
	"context"
	"sync"
	"testing"
)

func TestIsVisible(t *testing.T) {
	s := &Snapshot{ReadSeq: 10, TxnID: 42, InFlight: []TxnID{7, 8}}

	// Own writes are always visible, even in-flight (seq 0).
	if !s.IsVisible(0, 42) {
		t.Fatal("own in-flight write should be visible (read-your-writes)")
	}
	// A committed version at or before the read point is visible.
	if !s.IsVisible(10, 99) {
		t.Fatal("seq==ReadSeq from another txn should be visible")
	}
	if s.IsVisible(11, 99) {
		t.Fatal("seq past the read point must be invisible")
	}
	// A concurrently in-flight txn is invisible even if its seq fits.
	if s.IsVisible(5, 7) {
		t.Fatal("in-flight concurrent txn must be invisible")
	}
	// seq 0 from a foreign txn (uncommitted) is never visible.
	if s.IsVisible(0, 1234) {
		t.Fatal("foreign in-flight write must be invisible")
	}
}

func TestClockMonotonic(t *testing.T) {
	c := NewClock(5)
	if c.Current() != 5 {
		t.Fatalf("seeded current = %d, want 5", c.Current())
	}
	if got := c.NextSeq(); got != 6 {
		t.Fatalf("NextSeq = %d, want 6", got)
	}
	if got := c.NextSeq(); got != 7 {
		t.Fatalf("NextSeq = %d, want 7", got)
	}
	if a, b := c.NextTxn(), c.NextTxn(); a == b || b <= a {
		t.Fatalf("txn ids not strictly increasing: %d, %d", a, b)
	}
}

func TestVectorDeltaVisibilityWalk(t *testing.T) {
	d := NewVectorDelta()
	writer := TxnID(1)

	// Stage and commit v1 at seq 10.
	d.Put(100, writer, []float32{1, 2, 3})
	d.Commit(writer, 10)

	// A reader at seq 10 sees v1.
	r10 := &Snapshot{ReadSeq: 10, TxnID: 50}
	got, ok := d.Visible(100, r10)
	if !ok || got[0] != 1 {
		t.Fatalf("reader@10 should see v1, got %v ok=%v", got, ok)
	}

	// A reader at seq 9 sees no delta (falls through to base).
	r9 := &Snapshot{ReadSeq: 9, TxnID: 51}
	if _, ok := d.Visible(100, r9); ok {
		t.Fatal("reader@9 must not see v1 committed at 10")
	}

	// Overwrite with v2 at seq 20.
	w2 := TxnID(2)
	d.Put(100, w2, []float32{9, 9, 9})
	d.Commit(w2, 20)

	// reader@10 still sees v1 (snapshot isolation), reader@20 sees v2.
	if got, _ := d.Visible(100, r10); got[0] != 1 {
		t.Fatalf("reader@10 should still see v1 after v2 commit, got %v", got)
	}
	r20 := &Snapshot{ReadSeq: 20, TxnID: 52}
	if got, _ := d.Visible(100, r20); got[0] != 9 {
		t.Fatalf("reader@20 should see v2, got %v", got)
	}
}

func TestVectorDeltaTombstone(t *testing.T) {
	d := NewVectorDelta()
	d.Put(7, TxnID(1), []float32{1})
	d.Commit(TxnID(1), 10)
	d.Delete(7, TxnID(2))
	d.Commit(TxnID(2), 20)

	// After-delete reader sees nothing; pre-delete reader still sees the value.
	if _, ok := d.Visible(7, &Snapshot{ReadSeq: 20, TxnID: 9}); ok {
		t.Fatal("post-delete snapshot must not see the vector")
	}
	if _, ok := d.Visible(7, &Snapshot{ReadSeq: 10, TxnID: 9}); !ok {
		t.Fatal("pre-delete snapshot must still see the vector")
	}
}

func TestAbortDiscardsStaged(t *testing.T) {
	d := NewVectorDelta()
	d.Put(1, TxnID(1), []float32{1})
	d.Commit(TxnID(1), 10)
	d.Put(1, TxnID(2), []float32{2}) // in-flight overwrite
	d.Abort(TxnID(2))

	// The committed v1 survives; the aborted v2 left no trace.
	got, ok := d.Visible(1, &Snapshot{ReadSeq: 100, TxnID: 9})
	if !ok || got[0] != 1 {
		t.Fatalf("after abort, expected committed v1, got %v ok=%v", got, ok)
	}
}

func TestReadYourOwnWritesMidTxn(t *testing.T) {
	d := NewVectorDelta()
	txn := TxnID(5)
	d.Put(1, txn, []float32{42})
	// The writer reads its own uncommitted value (seq 0, own txn id).
	s := &Snapshot{ReadSeq: 0, TxnID: txn}
	got, ok := d.Visible(1, s)
	if !ok || got[0] != 42 {
		t.Fatalf("writer must read its own uncommitted write, got %v ok=%v", got, ok)
	}
	// A different concurrent txn must not see it.
	other := &Snapshot{ReadSeq: 0, TxnID: 6}
	if _, ok := d.Visible(1, other); ok {
		t.Fatal("foreign txn must not see uncommitted write")
	}
}

func TestIDMapBijection(t *testing.T) {
	m := NewIDMap()
	m.Assign(PointID(1000), 3, TxnID(1))
	m.Commit(TxnID(1), 10)

	s := &Snapshot{ReadSeq: 10, TxnID: 9}
	if pos, ok := m.Lookup(1000, s); !ok || pos != 3 {
		t.Fatalf("forward lookup = %d ok=%v, want 3", pos, ok)
	}
	if id, ok := m.Reverse(3, s); !ok || id != 1000 {
		t.Fatalf("reverse lookup = %d ok=%v, want 1000", id, ok)
	}

	// Delete tombstones both sides for later snapshots.
	m.Delete(1000, 3, TxnID(2))
	m.Commit(TxnID(2), 20)
	if _, ok := m.Lookup(1000, &Snapshot{ReadSeq: 20, TxnID: 9}); ok {
		t.Fatal("deleted id must be invisible to a post-delete snapshot")
	}
	if _, ok := m.Lookup(1000, s); !ok {
		t.Fatal("deleted id must still be visible to the pre-delete snapshot")
	}
}

func TestMetaDeltaVisibility(t *testing.T) {
	d := NewMetaDelta()
	d.Put(2, 5, TxnID(1), "hello")
	d.Commit(TxnID(1), 10)
	v, ok := d.Visible(2, 5, &Snapshot{ReadSeq: 10, TxnID: 9})
	if !ok || v.(string) != "hello" {
		t.Fatalf("meta visible = %v ok=%v, want hello", v, ok)
	}
	if _, ok := d.Visible(2, 5, &Snapshot{ReadSeq: 9, TxnID: 9}); ok {
		t.Fatal("meta committed at 10 must be invisible at 9")
	}
}

func TestWatermarkOracle(t *testing.T) {
	clk := NewClock(100)
	w := NewWatermarkOracle(clk)

	// No live readers: watermark is the current commit seq.
	if got := w.Watermark(); got != 100 {
		t.Fatalf("empty oracle watermark = %d, want 100", got)
	}
	w.Register(1, 50)
	w.Register(2, 70)
	if got := w.Watermark(); got != 50 {
		t.Fatalf("watermark = %d, want 50 (the minimum)", got)
	}
	w.Deregister(1)
	if got := w.Watermark(); got != 70 {
		t.Fatalf("watermark after deregister = %d, want 70", got)
	}
	w.Deregister(2)
	if got := w.Watermark(); got != 100 {
		t.Fatalf("watermark with no readers = %d, want 100", got)
	}
}

func TestGCFoldsBelowWatermark(t *testing.T) {
	d := NewVectorDelta()
	// Three committed versions at a position.
	d.Put(1, TxnID(1), []float32{1})
	d.Commit(TxnID(1), 10)
	d.Put(1, TxnID(2), []float32{2})
	d.Commit(TxnID(2), 20)
	d.Put(1, TxnID(3), []float32{3})
	d.Commit(TxnID(3), 30)

	// Watermark at 25: versions below 25 superseded by the <=25 base (seq 20) are
	// dropped, but the seq-30 version and the seq-20 base survive.
	d.GC(25)

	// A reader at 20 still sees the base v2; a reader at 30 sees v3.
	if got, ok := d.Visible(1, &Snapshot{ReadSeq: 20, TxnID: 9}); !ok || got[0] != 2 {
		t.Fatalf("post-GC reader@20 = %v ok=%v, want v2", got, ok)
	}
	if got, ok := d.Visible(1, &Snapshot{ReadSeq: 30, TxnID: 9}); !ok || got[0] != 3 {
		t.Fatalf("post-GC reader@30 = %v ok=%v, want v3", got, ok)
	}
}

func TestGCReclaimsDeadTombstone(t *testing.T) {
	d := NewVectorDelta()
	d.Put(1, TxnID(1), []float32{1})
	d.Commit(TxnID(1), 10)
	d.Delete(1, TxnID(2))
	d.Commit(TxnID(2), 20)

	if d.tbl.Len() != 1 {
		t.Fatalf("chain count before GC = %d, want 1", d.tbl.Len())
	}
	// Watermark past the tombstone: the key is fully dead and removed.
	d.GC(30)
	if d.tbl.Len() != 0 {
		t.Fatalf("dead tombstone chain not reclaimed, len = %d", d.tbl.Len())
	}
}

func TestWriteSetConflict(t *testing.T) {
	a := NewWriteSet()
	a.MarkModified(1)
	a.MarkModified(2)
	b := NewWriteSet()
	b.MarkModified(3)
	if a.ConflictsWith(b) {
		t.Fatal("disjoint write-sets must not conflict")
	}
	b.MarkModified(2)
	if !a.ConflictsWith(b) {
		t.Fatal("overlapping point id must conflict")
	}
}

func TestCommittedRingFirstCommitterWins(t *testing.T) {
	r := NewCommittedRing()

	// T_other (read@5) committed a write to point 7 at seq 6.
	wsOther := NewWriteSet()
	wsOther.MarkModified(7)
	r.Record(TxnID(1), 6, wsOther)

	// T_self read at seq 5 and also wrote point 7: it must abort (FCW).
	wsSelf := NewWriteSet()
	wsSelf.MarkModified(7)
	conflicter, err := r.Validate(5, wsSelf)
	if err != ErrWriteConflict || conflicter != 1 {
		t.Fatalf("expected conflict with txn 1, got txn=%d err=%v", conflicter, err)
	}

	// A transaction that wrote a disjoint point validates cleanly.
	wsDisjoint := NewWriteSet()
	wsDisjoint.MarkModified(99)
	if _, err := r.Validate(5, wsDisjoint); err != nil {
		t.Fatalf("disjoint write should not conflict, got %v", err)
	}

	// A transaction whose read point is at/after the committed seq does not
	// validate against it (it already reflects that write).
	if _, err := r.Validate(6, wsSelf); err != nil {
		t.Fatalf("read@6 already includes seq-6 commit, got %v", err)
	}
}

func TestCommittedRingPruneAndExpiry(t *testing.T) {
	r := NewCommittedRing()
	ws := NewWriteSet()
	ws.MarkModified(1)
	r.Record(TxnID(1), 10, ws)
	r.Record(TxnID(2), 11, ws)

	// Prune below watermark 11 drops the seq-10 entry.
	r.Prune(11)

	// A snapshot at read point 5 can no longer validate (oldest retained is seq 11,
	// which is > 5+1), so it must be told to retry.
	probe := NewWriteSet()
	probe.MarkModified(2)
	if _, err := r.Validate(5, probe); err != ErrSnapshotExpired {
		t.Fatalf("stale snapshot should expire, got %v", err)
	}
}

func TestWriterLockSerializes(t *testing.T) {
	l := NewWriterLock()
	ctx := context.Background()
	if err := l.Acquire(ctx); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if l.TryAcquire() {
		t.Fatal("second acquire should fail while held")
	}
	l.Release()
	if !l.TryAcquire() {
		t.Fatal("acquire after release should succeed")
	}
	l.Release()

	// Concurrent writers are serialized: a counter guarded only by the lock never
	// races.
	var n int
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Acquire(ctx)
			n++
			l.Release()
		}()
	}
	wg.Wait()
	if n != 50 {
		t.Fatalf("serialized counter = %d, want 50", n)
	}
}

func TestCancelableAcquire(t *testing.T) {
	l := NewWriterLock()
	_ = l.Acquire(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Acquire(ctx); err == nil {
		t.Fatal("acquire on a canceled context should fail")
	}
	l.Release()
}
