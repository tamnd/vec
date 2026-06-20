package vec

import (
	"context"
	"fmt"
	"io"

	"github.com/tamnd/vec/catalog"
	"github.com/tamnd/vec/storage"
)

// Collection is a stateless, goroutine-safe reference to a named collection
// (spec 14 §4.3). It carries no mutable state of its own; every operation resolves
// the live collection through the *DB.
type Collection struct {
	db   *DB
	name string
}

// Name returns the collection name.
func (c *Collection) Name() string { return c.name }

// Schema returns the current schema (spec 14 §4.5).
func (c *Collection) Schema(ctx context.Context) (CollectionSchema, error) {
	cs, err := c.db.state(c.name)
	if err != nil {
		return CollectionSchema{}, err
	}
	out := CollectionSchema{Name: c.name}
	for _, col := range cs.cc.Schema.Columns {
		out.Columns = append(out.Columns, columnDefFromCatalog(col))
	}
	return out, nil
}

// vectorColumn returns the single vector column name of the collection.
func vectorColumn(cc *catalog.Collection) string {
	vs := cc.Schema.VectorColumns()
	if len(vs) == 0 {
		return ""
	}
	return vs[0].Name
}

// hasVectorColumn reports whether name is a vector column of the collection.
func hasVectorColumn(cc *catalog.Collection, name string) bool {
	for _, v := range cc.Schema.VectorColumns() {
		if v.Name == name {
			return true
		}
	}
	return false
}

// denseFor extracts the dense vector for the collection's vector column from p.
func denseFor(cc *catalog.Collection, p Point) (string, Vector, error) {
	col := vectorColumn(cc)
	av, ok := p.Vectors[col]
	if !ok {
		// Allow a single-entry map under any key for the lone vector column.
		if len(p.Vectors) == 1 {
			for _, v := range p.Vectors {
				av = v
				ok = true
			}
		}
	}
	if !ok || av.Dense == nil {
		return col, nil, &SchemaError{Column: col, Reason: "point has no dense vector for the column"}
	}
	return col, av.Dense, nil
}

// writeOne resolves identity, validates, lowers metadata, and writes one point
// through txn. upsert selects Upsert vs Insert semantics.
func (c *Collection) writeOne(cs *collState, stx storage.Txn, p Point, upsert bool) (PointID, error) {
	cc := cs.cc
	col, dense, err := denseFor(cc, p)
	if err != nil {
		return PointID{}, err
	}
	if err := cc.ValidateVector(col, dense); err != nil {
		return PointID{}, mapWriteErr(c.name, err)
	}

	var supplied *catalog.PointID
	if p.ID.IsBytes {
		pid := catalog.BlobID(p.ID.B)
		supplied = &pid
	} else if p.ID.N != 0 {
		pid := catalog.BigIntID(p.ID.N)
		supplied = &pid
	}
	resolved, enginePID, err := cc.PrepareID(supplied)
	if err != nil {
		return PointID{}, mapWriteErr(c.name, err)
	}

	meta, err := cc.LowerMeta(toCatalogMeta(p.Meta), txnNow())
	if err != nil {
		return PointID{}, mapWriteErr(c.name, err)
	}

	if upsert {
		if _, _, err := c.db.engine.Upsert(stx, cc.ID, enginePID, dense, meta); err != nil {
			return PointID{}, mapWriteErr(c.name, err)
		}
	} else {
		if _, err := c.db.engine.Insert(stx, cc.ID, enginePID, dense, meta); err != nil {
			return PointID{}, mapWriteErr(c.name, err)
		}
	}
	return PointID{N: resolved.U, B: resolved.Bytes, IsBytes: resolved.Kind != catalog.IDBigInt}, nil
}

// Upsert inserts or replaces a point inside txn (spec 14 §4.4).
func (c *Collection) Upsert(txn *Txn, p Point) (PointID, error) {
	cs, err := c.requireWrite(txn)
	if err != nil {
		return PointID{}, err
	}
	return c.writeOne(cs, txn.stx, p, true)
}

// Insert inserts a point, failing with ErrAlreadyExists if the id exists.
func (c *Collection) Insert(txn *Txn, p Point) (PointID, error) {
	cs, err := c.requireWrite(txn)
	if err != nil {
		return PointID{}, err
	}
	return c.writeOne(cs, txn.stx, p, false)
}

// Delete deletes a point by id inside txn (spec 14 §4.4).
func (c *Collection) Delete(txn *Txn, id PointID) error {
	cs, err := c.requireWrite(txn)
	if err != nil {
		return err
	}
	if err := c.db.engine.Delete(txn.stx, cs.cc.ID, enginePID(id)); err != nil {
		return mapWriteErr(c.name, err)
	}
	cs.cc.NoteDeleted(catalog.BigIntID(id.N))
	return nil
}

// Get fetches a point by id (spec 14 §4.4).
func (c *Collection) Get(txn *Txn, id PointID) (Point, error) {
	cs, err := c.db.state(c.name)
	if err != nil {
		return Point{}, err
	}
	pos, err := c.db.engine.LookupID(cs.cc.ID, enginePID(id))
	if err != nil {
		return Point{}, fmt.Errorf("vec: collection %q: %w", c.name, ErrNotFound)
	}
	snap := txnSnap(txn, c.db)
	rec, err := c.db.engine.Fetch(cs.cc.ID, pos, allMetaCols(cs.cc), snap)
	if err != nil {
		return Point{}, fmt.Errorf("vec: collection %q: %w", c.name, ErrNotFound)
	}
	return pointFromRecord(cs.cc, rec), nil
}

// GetBatch fetches multiple points (spec 14 §4.4).
func (c *Collection) GetBatch(txn *Txn, ids []PointID) ([]Point, error) {
	out := make([]Point, 0, len(ids))
	for _, id := range ids {
		p, err := c.Get(txn, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// UpsertBatch writes many points in a single implicit transaction (spec 14 §4.4).
func (c *Collection) UpsertBatch(ctx context.Context, points []Point) ([]PointID, error) {
	ids := make([]PointID, len(points))
	err := c.db.Update(ctx, func(txn *Txn) error {
		cs, err := c.requireWrite(txn)
		if err != nil {
			return err
		}
		for i, p := range points {
			id, err := c.writeOne(cs, txn.stx, p, true)
			if err != nil {
				return err
			}
			ids[i] = id
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteBatch removes multiple points (spec 14 §4.4).
func (c *Collection) DeleteBatch(ctx context.Context, ids []PointID, opts ...BatchOpt) error {
	return c.db.Update(ctx, func(txn *Txn) error {
		for _, id := range ids {
			if err := c.Delete(txn, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Query begins a fluent ANN query builder over a dense vector column (spec 14 §5).
func (c *Collection) Query(column string, q Vector) *QueryBuilder {
	return &QueryBuilder{coll: c, column: column, vector: q, k: 10, allowFlat: true}
}

// SparseQuery begins a sparse ANN query builder (spec 14 §5). Sparse columns are
// not yet stored, so executing the builder returns ErrInvalidSparse.
func (c *Collection) SparseQuery(column string, q SparseVector) *QueryBuilder {
	return &QueryBuilder{coll: c, column: column, sparse: &q, k: 10, unsupported: ErrInvalidSparse}
}

// MultiQuery begins a multi-vector ANN query builder (spec 14 §5). Multi-vector
// columns are not yet stored, so executing the builder returns an unsupported error.
func (c *Collection) MultiQuery(column string, q MultiVector) *QueryBuilder {
	return &QueryBuilder{coll: c, column: column, multi: q, k: 10, unsupported: errUnsupported}
}

// Count returns the number of live points in the collection (spec 14 §4.4).
func (c *Collection) Count(ctx context.Context) (int64, error) {
	cs, err := c.db.state(c.name)
	if err != nil {
		return 0, err
	}
	st, err := c.db.engine.CollectionStats(cs.cc.ID)
	if err != nil {
		return 0, err
	}
	return int64(st.LivePoints), nil
}

// Export writes all points to w in the specified format (spec 14 §4.4). The
// export pipeline is delivered with the bulk subsystem (spec 17).
func (c *Collection) Export(ctx context.Context, w io.Writer, opts ExportOptions) error {
	return errUnsupported
}

// requireWrite validates the txn is a live writable transaction and returns the
// collection state.
func (c *Collection) requireWrite(txn *Txn) (*collState, error) {
	if txn == nil {
		return nil, ErrReadOnly
	}
	if txn.IsDone() {
		return nil, ErrClosed
	}
	if !txn.writable {
		return nil, ErrReadOnly
	}
	return c.db.state(c.name)
}

// toCatalogMeta lowers a public metadata map into catalog values.
func toCatalogMeta(meta map[string]Value) map[string]catalog.Value {
	if meta == nil {
		return nil
	}
	out := make(map[string]catalog.Value, len(meta))
	for k, v := range meta {
		out[k] = v.catalogValue()
	}
	return out
}

// allMetaCols returns the engine column ids of every metadata column.
func allMetaCols(cc *catalog.Collection) []storage.ColID {
	cols := cc.Schema.MetadataColumns()
	out := make([]storage.ColID, 0, len(cols))
	for _, col := range cols {
		if id, ok := cc.ColID(col.Name); ok {
			out = append(out, id)
		}
	}
	return out
}

// pointFromRecord assembles a public Point from an engine record.
func pointFromRecord(cc *catalog.Collection, rec storage.PointRecord) Point {
	p := Point{
		ID:      PointID{N: uint64(rec.ID)},
		Vectors: map[string]AnyVector{},
	}
	if rec.Vec != nil {
		p.Vectors[vectorColumn(cc)] = AnyVector{Dense: append(Vector(nil), rec.Vec...)}
	}
	if len(rec.Meta) > 0 {
		p.Meta = make(map[string]Value, len(rec.Meta))
		for _, col := range cc.Schema.MetadataColumns() {
			if id, ok := cc.ColID(col.Name); ok {
				if v, present := rec.Meta[id]; present {
					p.Meta[col.Name] = valueFromStorage(v)
				}
			}
		}
	}
	return p
}

// enginePID folds a public point id into the engine id space. The facade creates
// BIGINT-keyed collections, so an integer id passes through; a byte key folds
// through the same FNV-1a hash the catalog uses.
func enginePID(id PointID) storage.PointID {
	if !id.IsBytes {
		return storage.PointID(id.N)
	}
	return storage.PointID(fnv1a(id.B))
}

// fnv1a matches catalog.fnv1a so byte keys fold identically across layers.
func fnv1a(b []byte) uint64 {
	const (
		offset = 1469598103934665603
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime
	}
	return h
}

// mapWriteErr maps a catalog or engine write error to the library vocabulary.
func mapWriteErr(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("vec: collection %q: %w", name, classifyWriteErr(err))
}
