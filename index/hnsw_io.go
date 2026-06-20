package index

import (
	"encoding/binary"
	"math"
)

// hnswMagic identifies an HNSW state blob (spec 07 §8.1, "HNSW").
const hnswMagic uint32 = 0x484E5357

// Persist serializes the whole graph to one blob through the PageStore (spec 07
// §9.1). The on-disk page layout (§8) is the storage-engine slice; the blob form
// carries identical state: header, then per node its level, vector, and per-layer
// neighbor lists. Tombstoned nodes are dropped, matching a rebuild-on-persist.
func (h *HNSW) Persist(ps PageStore) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return ErrClosed
	}

	// Stable position order so the blob is reproducible.
	positions := make([]uint32, 0, len(h.nodes))
	for pos, n := range h.nodes {
		if n.deletedAt == 0 {
			positions = append(positions, pos)
		}
	}
	sortU32(positions)

	buf := make([]byte, 0, 64+len(positions)*(h.dim*4+64))
	buf = binary.LittleEndian.AppendUint32(buf, hnswMagic)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.dim))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.metric))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.m))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.m0))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(h.efConstruction))
	buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(h.mL))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(h.seed))
	ep := h.entrypoint
	buf = binary.LittleEndian.AppendUint32(buf, ep)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(int32(h.maxLayer)))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(positions)))

	for _, pos := range positions {
		n := h.nodes[pos]
		buf = binary.LittleEndian.AppendUint32(buf, pos)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(n.maxLevel))
		v := h.vecs[pos]
		for _, x := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(x))
		}
		for layer := 0; layer <= n.maxLevel; layer++ {
			nbrs := n.neighbors[layer]
			buf = binary.LittleEndian.AppendUint32(buf, uint32(len(nbrs)))
			for _, nb := range nbrs {
				buf = binary.LittleEndian.AppendUint32(buf, nb)
			}
		}
	}
	return ps.PutBlob(buf)
}

// Recover reads a blob written by Persist (spec 07 §9.2). It validates the magic
// and bounds every read so a truncated blob returns ErrIndexCorrupt rather than
// panicking.
func (h *HNSW) Recover(ps PageStore) error {
	b, err := ps.GetBlob()
	if err != nil {
		return err
	}
	r := &reader{b: b}
	if r.u32() != hnswMagic {
		return ErrIndexCorrupt
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.dim = int(r.u32())
	h.metric = Metric(r.u32())
	h.dist = metricDistance(h.metric)
	h.m = int(r.u32())
	h.m0 = int(r.u32())
	h.efConstruction = int(r.u32())
	h.mL = math.Float64frombits(r.u64())
	h.seed = int64(r.u64())
	h.entrypoint = r.u32()
	h.maxLayer = int(int32(r.u32()))
	count := int(r.u32())
	if r.err {
		return ErrIndexCorrupt
	}

	h.nodes = make(map[uint32]*hnswNode, count)
	h.vecs = make(map[uint32][]float32, count)
	h.codes = make(map[uint32][]byte)
	h.rng = nil // re-seeded lazily on the next Insert path

	for i := 0; i < count; i++ {
		pos := r.u32()
		level := int(r.u32())
		if r.err || level < 0 {
			return ErrIndexCorrupt
		}
		v := make([]float32, h.dim)
		for j := 0; j < h.dim; j++ {
			v[j] = math.Float32frombits(r.u32())
		}
		node := &hnswNode{maxLevel: level, neighbors: make([][]uint32, level+1)}
		for layer := 0; layer <= level; layer++ {
			m := int(r.u32())
			if r.err || m < 0 || m > h.m0+1 {
				return ErrIndexCorrupt
			}
			nbrs := make([]uint32, m)
			for k := 0; k < m; k++ {
				nbrs[k] = r.u32()
			}
			node.neighbors[layer] = nbrs
		}
		if r.err {
			return ErrIndexCorrupt
		}
		h.nodes[pos] = node
		h.vecs[pos] = v
		if h.codec != nil {
			code := make([]byte, h.codec.CodeSize())
			h.codec.Encode(v, code)
			h.codes[pos] = code
		}
	}
	if r.err {
		return ErrIndexCorrupt
	}
	// Lazily restore the RNG so post-recover inserts stay deterministic.
	h.ensureRNG()
	return nil
}

// reader is a bounds-checked little-endian cursor; once it reads past the end it
// latches err and returns zeros, so callers check err once after a batch.
type reader struct {
	b   []byte
	off int
	err bool
}

func (r *reader) u32() uint32 {
	if r.off+4 > len(r.b) {
		r.err = true
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) u64() uint64 {
	if r.off+8 > len(r.b) {
		r.err = true
		return 0
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

// sortU32 sorts a slice of positions ascending (small, insertion-free helper).
func sortU32(s []uint32) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
