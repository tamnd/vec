package pager

import (
	"fmt"

	"github.com/tamnd/vec/format"
	"github.com/tamnd/vec/vfs"
)

// Get returns the frame for pgno, pinned, reading it from the main file if it is
// not already resident. The caller must Unpin exactly once. intent is advisory in
// this milestone; dirtiness is declared at Unpin.
func (p *Pager) Get(pgno uint32, intent Intent) (*Frame, error) {
	if pgno == 0 {
		return nil, fmt.Errorf("pager: page 0 is the null page")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if fr, ok := p.index[pgno]; ok {
		fr.pins.Add(1)
		fr.ref = true
		return fr, nil
	}
	fr, err := p.admit(pgno)
	if err != nil {
		return nil, err
	}
	off := int64(pgno-1) * int64(p.pageSize)
	if _, err := p.file.ReadAt(fr.data, off); err != nil {
		// A short read at the tail of a just-grown file is not an error; zero-fill.
		for i := range fr.data {
			fr.data[i] = 0
		}
	}
	fr.pins.Add(1)
	fr.ref = true
	return fr, nil
}

// Unpin releases one pin. If dirty, the frame is marked for write-back at the
// next checkpoint.
func (p *Pager) Unpin(fr *Frame, dirty bool) {
	p.mu.Lock()
	if dirty {
		fr.dirty = true
	}
	fr.pins.Add(-1)
	p.mu.Unlock()
}

// admit finds a free or evictable frame, binds it to pgno, and indexes it. The
// caller must hold p.mu. The returned frame is not yet pinned.
func (p *Pager) admit(pgno uint32) (*Frame, error) {
	fr := p.evict()
	if fr == nil {
		return nil, fmt.Errorf("pager: buffer pool exhausted (all frames pinned)")
	}
	fr.pgno = pgno
	fr.dirty = false
	fr.ref = false
	p.index[pgno] = fr
	return fr, nil
}

// evict returns a reusable frame via CLOCK: sweep, clearing reference bits, and
// take the first unpinned frame whose bit is already clear. A dirty victim is
// written back to the main file first. The caller must hold p.mu.
func (p *Pager) evict() *Frame {
	for _, fr := range p.pool {
		if fr.pgno == 0 && fr.pins.Load() == 0 {
			return fr
		}
	}
	n := len(p.pool)
	for i := 0; i < 2*n; i++ {
		fr := p.pool[p.hand]
		p.hand = (p.hand + 1) % n
		if fr.pins.Load() != 0 {
			continue
		}
		if fr.ref {
			fr.ref = false
			continue
		}
		if fr.dirty {
			if err := p.writeBack(fr); err != nil {
				continue
			}
		}
		delete(p.index, fr.pgno)
		fr.pgno = 0
		fr.dirty = false
		return fr
	}
	return nil
}

// writeBack flushes one dirty frame to the main file, stamping its tail checksum
// first so the on-disk image is self-verifying (spec 03 §6.2). The caller must
// hold p.mu.
func (p *Pager) writeBack(fr *Frame) error {
	if fr.pgno != 1 { // page 1 is the header page; its checksum is the header's own
		format.WritePageChecksum(fr.data, p.header.Checksum)
	}
	off := int64(fr.pgno-1) * int64(p.pageSize)
	if _, err := p.file.WriteAt(fr.data, off); err != nil {
		return err
	}
	fr.dirty = false
	return nil
}

// Allocate returns a fresh page, pinned with Write intent and zeroed. It reuses a
// page from the freelist if one is available, otherwise it grows the file by one
// page (high-water mark).
func (p *Pager) Allocate() (uint32, *Frame, error) {
	p.mu.Lock()
	var pgno uint32
	if n := len(p.free); n > 0 {
		pgno = p.free[n-1]
		p.free = p.free[:n-1]
	} else {
		p.dbSize++
		pgno = p.dbSize
	}
	fr, err := p.admit(pgno)
	if err != nil {
		p.mu.Unlock()
		return 0, nil, err
	}
	for i := range fr.data {
		fr.data[i] = 0
	}
	fr.dirty = true
	fr.pins.Add(1)
	fr.ref = true
	p.mu.Unlock()
	return pgno, fr, nil
}

// Free returns a page to the freelist. The page must not be pinned.
func (p *Pager) Free(pgno uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if fr, ok := p.index[pgno]; ok {
		delete(p.index, pgno)
		fr.pgno = 0
		fr.dirty = false
		fr.ref = false
	}
	p.free = append(p.free, pgno)
}

// Checkpoint writes every dirty frame to the main file, persists the freelist and
// header, and fsyncs. After it returns, the main file is a consistent image of
// all committed work. vec's header carries no checkpoint-LSN field; idempotent
// redo is driven by the per-page page_lsn instead (spec 05), so checkpointLSN is
// accepted for API symmetry with the WAL but not stamped into the header here.
func (p *Pager) Checkpoint(checkpointLSN uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, fr := range p.pool {
		if fr.pgno != 0 && fr.dirty && fr.pgno != 1 {
			if err := p.writeBack(fr); err != nil {
				return err
			}
		}
	}
	if err := p.persistFreelistLocked(); err != nil {
		return err
	}
	// Update and write the header page.
	p.header.PageCount = p.dbSize
	p.header.ChangeCounter++
	p.header.VersionValidFor = p.header.ChangeCounter
	page1 := make([]byte, p.pageSize)
	if fr, ok := p.index[1]; ok {
		copy(page1, fr.data)
	} else {
		_, _ = p.file.ReadAt(page1, 0)
	}
	p.header.Encode(page1)
	if fr, ok := p.index[1]; ok {
		copy(fr.data, page1)
		fr.dirty = false
	}
	if _, err := p.file.WriteAt(page1, 0); err != nil {
		return err
	}
	return p.file.Sync(vfs.SyncFull)
}

// SetTxnHighWater records the highest committed MVCC transaction id in the header
// so it survives restart and is never reissued (spec 06). It is flushed at the
// next Checkpoint.
func (p *Pager) SetTxnHighWater(v uint64) {
	p.mu.Lock()
	if v > p.header.TxnHighWater {
		p.header.TxnHighWater = v
	}
	p.mu.Unlock()
}

// SetCatalogRoot records the catalog root page in the header (spec 03 §3.2). It
// is flushed at the next Checkpoint.
func (p *Pager) SetCatalogRoot(pgno uint32) {
	p.mu.Lock()
	p.header.CatalogRoot = format.PageNo(pgno)
	p.mu.Unlock()
}

// Close releases the file; it flushes nothing implicitly. Callers checkpoint
// first for a clean shutdown.
func (p *Pager) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.file.Close()
}

// loadFreelist reads the freelist trunk chain into memory. Called during Open
// before the pager is shared.
func (p *Pager) loadFreelist() error {
	trunk := uint32(p.header.FreelistTrunk)
	buf := make([]byte, p.pageSize)
	for trunk != 0 {
		off := int64(trunk-1) * int64(p.pageSize)
		if _, err := p.file.ReadAt(buf, off); err != nil {
			return fmt.Errorf("pager: read freelist trunk %d: %w", trunk, err)
		}
		tp := format.DecodeTrunk(buf)
		p.free = append(p.free, tp.Leafs...)
		p.free = append(p.free, trunk) // the trunk page itself is free once drained
		trunk = tp.Next
	}
	return nil
}

// persistFreelistLocked writes the in-memory freelist back as a trunk chain. The
// caller must hold p.mu. This milestone rebuilds the whole chain each checkpoint.
func (p *Pager) persistFreelistLocked() error {
	cap := format.TrunkCapacity(p.pageSize, p.header.ReservedPerPage)
	if len(p.free) == 0 || cap == 0 {
		p.header.FreelistTrunk = 0
		p.header.FreelistCount = 0
		return nil
	}
	free := append([]uint32(nil), p.free...)
	var trunks []uint32
	for {
		nTrunks := len(trunks)
		leaves := len(free) - nTrunks
		need := (leaves + cap - 1) / cap
		if need <= nTrunks {
			break
		}
		trunks = append(trunks, free[len(free)-1-nTrunks])
	}
	nTrunks := len(trunks)
	leaves := free[:len(free)-nTrunks]
	trunkPages := free[len(free)-nTrunks:]

	buf := make([]byte, p.pageSize)
	var next uint32
	li := 0
	for ti := 0; ti < len(trunkPages); ti++ {
		end := li + cap
		if end > len(leaves) {
			end = len(leaves)
		}
		tp := format.TrunkPage{Next: next, Leafs: leaves[li:end]}
		li = end
		format.EncodeTrunk(buf, tp)
		format.WritePageChecksum(buf, p.header.Checksum)
		off := int64(trunkPages[ti]-1) * int64(p.pageSize)
		if _, err := p.file.WriteAt(buf, off); err != nil {
			return err
		}
		next = trunkPages[ti]
	}
	p.header.FreelistTrunk = format.PageNo(next)
	p.header.FreelistCount = uint32(len(p.free))
	return nil
}
