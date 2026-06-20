package index

import (
	"encoding/binary"
	"math"

	"github.com/tamnd/vector/quant"
)

// ivfMagic guards an IVF index blob (spec 08 §18.1, blob form of the catalog
// entry). "IVF1" little-endian.
const ivfMagic uint32 = 0x31465649

// Persist serializes the whole IVF index to one PageStore blob (spec 08 §13.2,
// §18.3). The spec's per-list page layout (spec 08 §2.7) is the storage-engine
// slice; the blob carries the same state the pager will eventually hold as a page
// run. Tombstoned positions are dropped, matching a rebuild-on-persist.
func (idx *IVF) Persist(ps PageStore) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var b []byte
	b = binary.LittleEndian.AppendUint32(b, ivfMagic)
	b = binary.LittleEndian.AppendUint32(b, uint32(idx.dim))
	b = binary.LittleEndian.AppendUint32(b, uint32(idx.metric))
	b = binary.LittleEndian.AppendUint32(b, uint32(idx.nlist))
	b = binary.LittleEndian.AppendUint32(b, uint32(idx.nprobe))

	// Codec descriptor: kind (0xFF = none) so Recover can rebuild the adapter.
	codeSize := 0
	if idx.codec != nil {
		b = append(b, byte(idx.codec.CodecKind()))
		codeSize = idx.codec.CodeSize()
	} else {
		b = append(b, 0xFF)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(codeSize))

	// Centroids.
	for _, x := range idx.centroids {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
	}

	// Codebook (if any), via the quant codebook page.
	if idx.codec != nil {
		cb, err := quant.MarshalQuantizer(idx.codec)
		if err != nil {
			return err
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(len(cb)))
		b = append(b, cb...)
	}

	// Posting lists: per cell, a live-entry count then each entry. Each entry is
	// position, the code bytes (codeSize), and the full-precision vector for rerank
	// and plain-IVF scan.
	for c := 0; c < idx.nlist; c++ {
		live := make([]ivfEntry, 0, len(idx.lists[c]))
		for _, e := range idx.lists[c] {
			if _, dead := idx.deleted[e.pos]; !dead {
				live = append(live, e)
			}
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(len(live)))
		for _, e := range live {
			b = binary.LittleEndian.AppendUint32(b, e.pos)
			if codeSize > 0 {
				b = append(b, e.code...)
			}
			for _, x := range idx.vecs[e.pos] {
				b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
			}
		}
	}
	return ps.PutBlob(b)
}

// Recover rebuilds an IVF index from a blob written by Persist (spec 08 §13.2).
func (idx *IVF) Recover(ps PageStore) error {
	b, err := ps.GetBlob()
	if err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	r := &reader{b: b}
	if r.u32() != ivfMagic {
		return ErrIndexCorrupt
	}
	idx.dim = int(r.u32())
	idx.metric = Metric(r.u32())
	idx.dist = metricDistance(idx.metric)
	idx.nlist = int(r.u32())
	idx.nprobe = int(r.u32())
	if idx.nprobe <= 0 {
		idx.nprobe = defaultNProbe
	}
	kind := r.u8()
	codeSize := int(r.u32())
	if r.err {
		return ErrIndexCorrupt
	}

	idx.centroids = make([]float32, idx.nlist*idx.dim)
	for i := range idx.centroids {
		idx.centroids[i] = math.Float32frombits(r.u32())
	}

	idx.codec = nil
	if kind != 0xFF {
		clen := int(r.u32())
		if r.err || clen < 0 || r.off+clen > len(b) {
			return ErrIndexCorrupt
		}
		page := b[r.off : r.off+clen]
		r.off += clen
		codec, err := quant.UnmarshalQuantizer(quant.CodecKind(kind), page)
		if err != nil {
			return ErrIndexCorrupt
		}
		idx.codec = codec
	}

	idx.lists = make([][]ivfEntry, idx.nlist)
	idx.assignOf = make(map[uint32]int)
	idx.vecs = make(map[uint32][]float32)
	idx.deleted = make(map[uint32]struct{})
	for c := 0; c < idx.nlist; c++ {
		cnt := int(r.u32())
		if r.err || cnt < 0 {
			return ErrIndexCorrupt
		}
		idx.lists[c] = make([]ivfEntry, 0, cnt)
		for i := 0; i < cnt; i++ {
			pos := r.u32()
			var code []byte
			if codeSize > 0 {
				if r.off+codeSize > len(b) {
					return ErrIndexCorrupt
				}
				code = make([]byte, codeSize)
				copy(code, b[r.off:r.off+codeSize])
				r.off += codeSize
			}
			v := make([]float32, idx.dim)
			for j := range v {
				v[j] = math.Float32frombits(r.u32())
			}
			if r.err {
				return ErrIndexCorrupt
			}
			idx.lists[c] = append(idx.lists[c], ivfEntry{pos: pos, code: code})
			idx.assignOf[pos] = c
			idx.vecs[pos] = v
		}
	}
	return nil
}
