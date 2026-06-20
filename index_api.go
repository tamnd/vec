package vec

import (
	"context"
	"fmt"

	"github.com/tamnd/vec/index"
	"github.com/tamnd/vec/query"
)

// IndexSpec describes an index to create on a collection (spec 14 §6.1).
type IndexSpec struct {
	Name   string
	Column string
	Type   IndexType
	Params IndexParams
}

// CreateIndex creates and builds an ANN index over a collection's vector column
// (spec 14 §6.1). In this build CREATE INDEX populates the index synchronously from
// the live points; BuildIndex and Reindex rebuild it.
func (db *DB) CreateIndex(ctx context.Context, collection string, spec IndexSpec) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.cfg.readOnly {
		return ErrReadOnly
	}
	cs, ok := db.colls[collection]
	if !ok {
		return fmt.Errorf("vec: collection %q: %w", collection, ErrNotFound)
	}
	col := spec.Column
	if col == "" {
		col = vectorColumn(cs.cc)
	}
	if !hasVectorColumn(cs.cc, col) {
		return fmt.Errorf("vec: collection %q: column %q: %w", collection, col, ErrUnknownColumn)
	}
	cs.idxName = spec.Name
	cs.idxType = spec.Type
	cs.idxColumn = col
	cs.idxParams = spec.Params
	return db.buildIndexLocked(ctx, cs)
}

// BuildIndex rebuilds the index recorded on the collection (spec 14 §6.2).
func (db *DB) BuildIndex(ctx context.Context, collection string) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	cs, ok := db.colls[collection]
	if !ok {
		return fmt.Errorf("vec: collection %q: %w", collection, ErrNotFound)
	}
	if cs.idxName == "" {
		return fmt.Errorf("vec: collection %q: %w", collection, ErrNotFound)
	}
	return db.buildIndexLocked(ctx, cs)
}

// Reindex is an alias for BuildIndex that rebuilds from scratch (spec 14 §6.2).
func (db *DB) Reindex(ctx context.Context, collection string) error {
	return db.BuildIndex(ctx, collection)
}

// BuildIndexAsync starts an index build and returns a handle to await it (spec 14
// §6.6). The embedded build is synchronous, so the returned handle is already done.
func (db *DB) BuildIndexAsync(ctx context.Context, collection string) (*IndexBuild, error) {
	err := db.BuildIndex(ctx, collection)
	return &IndexBuild{done: true, err: err}, err
}

// buildIndexLocked constructs the configured index and populates it from the live
// points of the collection. db.mu must be held.
func (db *DB) buildIndexLocked(ctx context.Context, cs *collState) error {
	def := cs.cc.StorageDef()
	dim := int(def.Dims)

	m := paramInt(cs.idxParams, "m", 16)
	efc := paramInt(cs.idxParams, "ef_construction", 200)
	nlist := paramInt(cs.idxParams, "nlist", 0)
	nprobe := paramInt(cs.idxParams, "nprobe", 0)
	pqm := paramInt(cs.idxParams, "pq_m", 0)

	if cs.index != nil {
		_ = cs.index.Close()
		cs.index = nil
	}

	switch cs.idxType {
	case IndexFlat:
		// The flat path needs no built index; the executor scans vectors directly.
		cs.idxKind = query.PathFlat
		cs.m, cs.efc, cs.nlist = 0, 0, 0
		return nil
	case IndexHNSW:
		h, err := index.NewHNSW(index.HNSWConfig{Dim: dim, Metric: def.Metric, M: m, EfConstruction: efc})
		if err != nil {
			return fmt.Errorf("vec: collection %q: %w: %v", cs.cc.Schema.Name, ErrUnknownParam, err)
		}
		cs.index = h
		cs.idxKind = query.PathHNSW
		cs.m, cs.efc = m, efc
	case IndexIVFFlat, IndexIVFPQ:
		cfg := index.IVFConfig{Dim: dim, Metric: def.Metric, NList: nlist, NProbe: nprobe}
		if cs.idxType == IndexIVFPQ {
			if pqm <= 0 {
				pqm = 8
			}
			cfg.PQM = pqm
		}
		v, err := index.NewIVF(cfg)
		if err != nil {
			return fmt.Errorf("vec: collection %q: %w: %v", cs.cc.Schema.Name, ErrUnknownParam, err)
		}
		cs.index = v
		cs.idxKind = query.PathIVF
		cs.nlist = nlist
	default:
		return errUnsupported // DiskANN needs a page store (spec 08)
	}

	positions, vectors, err := db.collectVectors(cs)
	if err != nil {
		return err
	}
	vectorAt := func(pos uint32) []float32 { return vectors[pos] }
	bp := index.BuildParams{M: m, EfConstruction: efc, Metric: def.Metric}
	if err := cs.index.Build(ctx, positions, vectorAt, bp); err != nil {
		_ = cs.index.Close()
		cs.index = nil
		cs.idxKind = query.PathFlat
		return fmt.Errorf("vec: collection %q: index build: %w", cs.cc.Schema.Name, err)
	}
	return nil
}

// collectVectors materializes every live vector of cs keyed by engine position.
func (db *DB) collectVectors(cs *collState) ([]uint32, map[uint32][]float32, error) {
	positions := make([]uint32, 0)
	vectors := make(map[uint32][]float32)
	err := db.engine.ScanVectors(cs.cc.ID, db.engine.Snapshot(), func(pos uint32, vec []float32) bool {
		positions = append(positions, pos)
		vectors[pos] = append([]float32(nil), vec...)
		return true
	})
	if err != nil {
		return nil, nil, fmt.Errorf("vec: collection %q: scan vectors: %w", cs.cc.Schema.Name, err)
	}
	return positions, vectors, nil
}

// DropIndex drops the index on a collection (spec 14 §6.7).
func (db *DB) DropIndex(ctx context.Context, collection, name string) error {
	if err := ctx.Err(); err != nil {
		return ctxErr(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.cfg.readOnly {
		return ErrReadOnly
	}
	cs, ok := db.colls[collection]
	if !ok {
		return fmt.Errorf("vec: collection %q: %w", collection, ErrNotFound)
	}
	if cs.idxName == "" || (name != "" && cs.idxName != name) {
		return fmt.Errorf("vec: index %q: %w", name, ErrNotFound)
	}
	if cs.index != nil {
		_ = cs.index.Close()
		cs.index = nil
	}
	cs.idxName, cs.idxColumn, cs.idxParams = "", "", nil
	cs.idxType = IndexFlat
	cs.idxKind = query.PathFlat
	cs.m, cs.efc, cs.nlist = 0, 0, 0
	return nil
}

// ListIndexes lists the indexes on a collection (spec 14 §6.4).
func (db *DB) ListIndexes(ctx context.Context, collection string) ([]IndexInfo, error) {
	cs, err := db.state(collection)
	if err != nil {
		return nil, err
	}
	if cs.idxName == "" {
		return nil, nil
	}
	return []IndexInfo{{
		Name:   cs.idxName,
		Column: cs.idxColumn,
		Type:   cs.idxType,
		Params: cs.idxParams,
	}}, nil
}

// IndexStats reports live statistics for the index on a collection (spec 14 §6.5).
func (db *DB) IndexStats(ctx context.Context, collection string) (IndexStatsDetail, error) {
	cs, err := db.state(collection)
	if err != nil {
		return IndexStatsDetail{}, err
	}
	if cs.idxName == "" || cs.index == nil {
		return IndexStatsDetail{}, fmt.Errorf("vec: collection %q: %w", collection, ErrNotFound)
	}
	st := cs.index.Stats()
	return IndexStatsDetail{
		Name:           cs.idxName,
		Type:           cs.idxType,
		NodeCount:      st.NodeCount,
		TombstoneCount: st.TombstoneCount,
		MemoryBytes:    st.MemoryBytes,
	}, nil
}

// IndexBuild is a handle to an asynchronous index build (spec 14 §6.6).
type IndexBuild struct {
	done bool
	err  error
}

// Wait blocks until the build finishes and returns its error.
func (b *IndexBuild) Wait() error { return b.err }

// Done reports whether the build has finished.
func (b *IndexBuild) Done() bool { return b.done }

// Progress returns the latest build progress.
func (b *IndexBuild) Progress() IndexBuildStats {
	return IndexBuildStats{Phase: "done"}
}

// paramInt reads an integer index parameter, accepting int and float64 values.
func paramInt(p IndexParams, key string, def int) int {
	v, ok := p[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return def
	}
}
