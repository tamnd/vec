package index

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
)

// spannMagic guards a SPANN index blob (spec 08 §18.1). "SPN1".
const spannMagic uint32 = 0x314E5053

// Persist serializes the whole index to one PageStore blob (spec 08 §13.2, §18.3).
// The centroid index is not serialized: it is a deterministic HNSW over the
// centroids and is rebuilt on Recover, so the blob carries only centroids, vectors,
// and posting lists. Tombstoned positions are dropped, matching rebuild-on-persist.
func (s *SPANN) Persist(ps PageStore) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Stable position order so the blob is reproducible.
	positions := make([]uint32, 0, len(s.vecs))
	for pos := range s.vecs {
		if _, dead := s.deleted[pos]; dead {
			continue
		}
		positions = append(positions, pos)
	}
	sort.Slice(positions, func(a, b int) bool { return positions[a] < positions[b] })
	keep := make(map[uint32]struct{}, len(positions))
	for _, p := range positions {
		keep[p] = struct{}{}
	}

	var b []byte
	b = binary.LittleEndian.AppendUint32(b, spannMagic)
	b = binary.LittleEndian.AppendUint32(b, uint32(s.dim))
	b = binary.LittleEndian.AppendUint32(b, uint32(s.metric))
	b = binary.LittleEndian.AppendUint32(b, uint32(s.nlist))
	b = binary.LittleEndian.AppendUint32(b, uint32(s.nprobe))
	b = binary.LittleEndian.AppendUint32(b, uint32(s.replicaCount))
	b = binary.LittleEndian.AppendUint64(b, math.Float64bits(s.boundaryEps))
	b = binary.LittleEndian.AppendUint64(b, uint64(s.seed))
	b = binary.LittleEndian.AppendUint32(b, uint32(s.kmeansIt))

	// Centroids.
	for _, x := range s.centroids {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
	}

	// Vectors, in position order.
	b = binary.LittleEndian.AppendUint32(b, uint32(len(positions)))
	for _, pos := range positions {
		b = binary.LittleEndian.AppendUint32(b, pos)
		for _, x := range s.vecs[pos] {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
		}
	}

	// Posting lists, one per centroid, live members only.
	for c := 0; c < s.nlist; c++ {
		lst := s.lists[c]
		live := make([]uint32, 0, len(lst))
		for _, e := range lst {
			if _, ok := keep[e.pos]; ok {
				live = append(live, e.pos)
			}
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(len(live)))
		for _, pos := range live {
			b = binary.LittleEndian.AppendUint32(b, pos)
		}
	}
	return ps.PutBlob(b)
}

// Recover rebuilds a SPANN index from a blob written by Persist, reconstructing the
// centroid index from the centroids (spec 08 §13.2). Bounds-checked throughout.
func (s *SPANN) Recover(ps PageStore) error {
	b, err := ps.GetBlob()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	r := &reader{b: b}
	if r.u32() != spannMagic {
		return ErrIndexCorrupt
	}
	s.dim = int(r.u32())
	s.metric = Metric(r.u32())
	s.dist = metricDistance(s.metric)
	s.nlist = int(r.u32())
	s.nprobe = int(r.u32())
	s.replicaCount = int(r.u32())
	s.boundaryEps = math.Float64frombits(r.u64())
	s.seed = int64(r.u64())
	s.kmeansIt = int(r.u32())
	if r.err || s.dim <= 0 || s.nlist < 0 {
		return ErrIndexCorrupt
	}
	if s.nprobe <= 0 {
		s.nprobe = defaultNProbe
	}
	if s.replicaCount <= 0 {
		s.replicaCount = defaultSpannReplicas
	}
	if s.boundaryEps <= 0 {
		s.boundaryEps = defaultBoundaryEps
	}

	s.centroids = make([]float32, s.nlist*s.dim)
	for i := range s.centroids {
		s.centroids[i] = math.Float32frombits(r.u32())
	}

	nvec := int(r.u32())
	if r.err || nvec < 0 {
		return ErrIndexCorrupt
	}
	s.vecs = make(map[uint32][]float32, nvec)
	s.deleted = make(map[uint32]struct{})
	for i := 0; i < nvec; i++ {
		pos := r.u32()
		v := make([]float32, s.dim)
		for j := range v {
			v[j] = math.Float32frombits(r.u32())
		}
		if r.err {
			return ErrIndexCorrupt
		}
		s.vecs[pos] = v
	}

	s.lists = make(map[int][]spannEntry, s.nlist)
	for c := 0; c < s.nlist; c++ {
		cnt := int(r.u32())
		if r.err || cnt < 0 {
			return ErrIndexCorrupt
		}
		lst := make([]spannEntry, 0, cnt)
		for i := 0; i < cnt; i++ {
			lst = append(lst, spannEntry{pos: r.u32()})
		}
		if r.err {
			return ErrIndexCorrupt
		}
		s.lists[c] = lst
	}
	if r.err {
		return ErrIndexCorrupt
	}

	// Rebuild the centroid index deterministically from the centroids.
	s.centroidIndex = nil
	if s.nlist > 0 {
		ci, err := NewHNSW(HNSWConfig{Dim: s.dim, Metric: s.metric, Seed: s.seed})
		if err != nil {
			return err
		}
		cpos := make([]uint32, s.nlist)
		for c := 0; c < s.nlist; c++ {
			cpos[c] = uint32(c)
		}
		centroidAt := func(pos uint32) []float32 {
			return s.centroids[int(pos)*s.dim : (int(pos)+1)*s.dim]
		}
		if err := ci.Build(context.Background(), cpos, centroidAt, BuildParams{Metric: s.metric, Seed: s.seed}); err != nil {
			return err
		}
		s.centroidIndex = ci
	}
	return nil
}
