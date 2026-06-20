package index

import (
	"encoding/binary"
	"math"

	"github.com/tamnd/vector/quant"
)

// vamanaMagic guards a Vamana/DiskANN index blob (spec 08 §18.1). "VAM1".
const vamanaMagic uint32 = 0x314D4156

// Persist serializes the whole graph to one PageStore blob (spec 08 §13.2, §18.3).
// The spec's on-disk adjacency-block layout (spec 08 §8.7) is the storage-engine
// slice; the blob carries the same state: header, optional codec page, then per node
// its colocated vector and adjacency. Tombstoned nodes are dropped, matching a
// rebuild-on-persist, and surviving edges into them are filtered out.
func (g *Vamana) Persist(ps PageStore) error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Stable node order so the blob is reproducible.
	live := make([]uint32, 0, len(g.order))
	for _, pos := range g.order {
		if _, dead := g.deleted[pos]; dead {
			continue
		}
		if _, ok := g.vecs[pos]; ok {
			live = append(live, pos)
		}
	}

	var b []byte
	b = binary.LittleEndian.AppendUint32(b, vamanaMagic)
	b = binary.LittleEndian.AppendUint32(b, uint32(g.dim))
	b = binary.LittleEndian.AppendUint32(b, uint32(g.metric))
	b = binary.LittleEndian.AppendUint32(b, uint32(g.r))
	b = binary.LittleEndian.AppendUint32(b, uint32(g.l))
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(g.alpha))
	b = binary.LittleEndian.AppendUint32(b, uint32(g.beamWidth))
	b = binary.LittleEndian.AppendUint64(b, uint64(g.seed))
	b = binary.LittleEndian.AppendUint32(b, g.medoid)

	// Codec descriptor: kind (0xFF = none) and code size.
	codeSize := 0
	if g.codec != nil {
		b = append(b, byte(g.codec.CodecKind()))
		codeSize = g.codec.CodeSize()
	} else {
		b = append(b, 0xFF)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(codeSize))
	if g.codec != nil {
		cb, err := quant.MarshalQuantizer(g.codec)
		if err != nil {
			return err
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(len(cb)))
		b = append(b, cb...)
	}

	b = binary.LittleEndian.AppendUint32(b, uint32(len(live)))
	for _, pos := range live {
		b = binary.LittleEndian.AppendUint32(b, pos)
		for _, x := range g.vecs[pos] {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
		}
		if codeSize > 0 {
			b = append(b, g.codes[pos]...)
		}
		// Adjacency, filtered to live targets.
		nbrs := g.neighbors[pos]
		kept := make([]uint32, 0, len(nbrs))
		for _, nb := range nbrs {
			if _, dead := g.deleted[nb]; dead {
				continue
			}
			if _, ok := g.vecs[nb]; ok {
				kept = append(kept, nb)
			}
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(len(kept)))
		for _, nb := range kept {
			b = binary.LittleEndian.AppendUint32(b, nb)
		}
	}
	return ps.PutBlob(b)
}

// Recover rebuilds a Vamana graph from a blob written by Persist (spec 08 §13.2).
// Every read is bounds-checked so a truncated blob returns ErrIndexCorrupt.
func (g *Vamana) Recover(ps PageStore) error {
	b, err := ps.GetBlob()
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	r := &reader{b: b}
	if r.u32() != vamanaMagic {
		return ErrIndexCorrupt
	}
	g.dim = int(r.u32())
	g.metric = Metric(r.u32())
	g.dist = metricDistance(g.metric)
	g.r = int(r.u32())
	g.l = int(r.u32())
	g.alpha = math.Float64frombits(r.u64())
	g.beamWidth = int(r.u32())
	g.seed = int64(r.u64())
	g.medoid = r.u32()
	kind := r.u8()
	codeSize := int(r.u32())
	if r.err {
		return ErrIndexCorrupt
	}
	if g.beamWidth <= 0 {
		g.beamWidth = defaultBeamWidth
	}

	g.codec = nil
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
		g.codec = codec
	}

	count := int(r.u32())
	if r.err || count < 0 {
		return ErrIndexCorrupt
	}
	g.neighbors = make(map[uint32][]uint32, count)
	g.vecs = make(map[uint32][]float32, count)
	g.codes = make(map[uint32][]byte)
	g.deleted = make(map[uint32]struct{})
	g.order = make([]uint32, 0, count)

	for i := 0; i < count; i++ {
		pos := r.u32()
		v := make([]float32, g.dim)
		for j := range v {
			v[j] = math.Float32frombits(r.u32())
		}
		var code []byte
		if codeSize > 0 {
			if r.off+codeSize > len(b) {
				return ErrIndexCorrupt
			}
			code = make([]byte, codeSize)
			copy(code, b[r.off:r.off+codeSize])
			r.off += codeSize
		}
		m := int(r.u32())
		if r.err || m < 0 {
			return ErrIndexCorrupt
		}
		nbrs := make([]uint32, m)
		for j := 0; j < m; j++ {
			nbrs[j] = r.u32()
		}
		if r.err {
			return ErrIndexCorrupt
		}
		g.vecs[pos] = v
		if code != nil {
			g.codes[pos] = code
		}
		g.neighbors[pos] = nbrs
		g.order = append(g.order, pos)
	}
	if r.err {
		return ErrIndexCorrupt
	}
	return nil
}
