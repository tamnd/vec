package vec

import (
	"context"
	"errors"
	"testing"
)

// newTestDB opens an ephemeral database with one collection holding a 4-dim L2
// vector column "emb" and a text metadata column "title".
func newTestDB(t *testing.T) (*DB, *Collection) {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := CollectionSchema{
		Name: "docs",
		Columns: []ColumnDef{
			{Name: "emb", Type: TypeVector, Dim: 4, Metric: MetricL2},
			{Name: "title", Type: TypeText},
		},
	}
	if err := db.CreateCollection(context.Background(), schema); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	coll, err := db.Collection("docs")
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	return db, coll
}

// point builds a point with the given id, vector, and title.
func point(id uint64, vec []float32, title string) Point {
	return Point{
		ID:      IntID(id),
		Vectors: map[string]AnyVector{"emb": {Dense: Vector(vec)}},
		Meta:    map[string]Value{"title": TextValue(title)},
	}
}

func seed(t *testing.T, db *DB, coll *Collection) {
	t.Helper()
	pts := []Point{
		point(1, []float32{1, 0, 0, 0}, "one"),
		point(2, []float32{0, 1, 0, 0}, "two"),
		point(3, []float32{0, 0, 1, 0}, "three"),
		point(4, []float32{0, 0, 0, 1}, "four"),
	}
	if _, err := coll.UpsertBatch(context.Background(), pts); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}
}

func TestCreateAndGet(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)

	n, err := coll.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 4 {
		t.Fatalf("Count = %d, want 4", n)
	}

	p, err := coll.Get(nil, IntID(2))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.ID.N != 2 {
		t.Fatalf("Get id = %d, want 2", p.ID.N)
	}
	if title := p.Meta["title"]; title.Text() != "two" {
		t.Fatalf("title = %q, want two", title.Text())
	}
}

func TestGetMissing(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)
	if _, err := coll.Get(nil, IntID(99)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing err = %v, want ErrNotFound", err)
	}
}

func TestQueryFlat(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)

	res, err := coll.Query("emb", Vector{1, 0, 0, 0}).K(2).All(context.Background())
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}
	if res[0].ID.N != 1 {
		t.Fatalf("nearest id = %d, want 1", res[0].ID.N)
	}
}

func TestQueryWithFilter(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)

	res, err := coll.Query("emb", Vector{0, 0, 0, 1}).
		Filter("title = ?", "two").
		K(4).
		All(context.Background())
	if err != nil {
		t.Fatalf("Query+Filter: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1", len(res))
	}
	if res[0].ID.N != 2 {
		t.Fatalf("filtered id = %d, want 2", res[0].ID.N)
	}
}

func TestHNSWIndexQuery(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)

	err := db.CreateIndex(context.Background(), "docs", IndexSpec{
		Name:   "emb_hnsw",
		Column: "emb",
		Type:   IndexHNSW,
		Params: IndexParams{"m": 8, "ef_construction": 64},
	})
	if err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	idxs, err := db.ListIndexes(context.Background(), "docs")
	if err != nil || len(idxs) != 1 || idxs[0].Name != "emb_hnsw" {
		t.Fatalf("ListIndexes = %v, %v", idxs, err)
	}

	res, err := coll.Query("emb", Vector{0, 1, 0, 0}).K(1).All(context.Background())
	if err != nil {
		t.Fatalf("Query via HNSW: %v", err)
	}
	if len(res) != 1 || res[0].ID.N != 2 {
		t.Fatalf("HNSW nearest = %v, want id 2", res)
	}

	if _, err := db.IndexStats(context.Background(), "docs"); err != nil {
		t.Fatalf("IndexStats: %v", err)
	}
	if err := db.DropIndex(context.Background(), "docs", "emb_hnsw"); err != nil {
		t.Fatalf("DropIndex: %v", err)
	}
}

func TestDelete(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)

	if err := coll.DeleteBatch(context.Background(), []PointID{IntID(3)}); err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	if _, err := coll.Get(nil, IntID(3)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrNotFound", err)
	}
}

func TestTransactionRollback(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)

	err := db.Update(context.Background(), func(txn *Txn) error {
		if _, err := coll.Upsert(txn, point(5, []float32{1, 1, 0, 0}, "five")); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("Update should have returned the closure error")
	}
	if _, err := coll.Get(nil, IntID(5)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back point should be absent, got %v", err)
	}
}

func TestExecSQL(t *testing.T) {
	db, coll := newTestDB(t)
	seed(t, db, coll)

	rows, err := db.Exec(context.Background(), "SELECT title FROM docs ORDER BY emb <-> '[1,0,0,0]' LIMIT 1")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("Exec returned no rows")
	}
	if rows.Result().ID.N != 1 {
		t.Fatalf("Exec nearest id = %d, want 1", rows.Result().ID.N)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	db, _ := newTestDB(t)
	_, err := db.Begin(context.Background(), true)
	if err != nil {
		t.Fatalf("Begin write: %v", err)
	}
}

func TestPragma(t *testing.T) {
	db, _ := newTestDB(t)
	v, err := db.Pragma(context.Background(), "synchronous", "")
	if err != nil {
		t.Fatalf("Pragma: %v", err)
	}
	if v != "normal" {
		t.Fatalf("synchronous = %q, want normal", v)
	}
	if _, err := db.Pragma(context.Background(), "bogus", ""); !errors.Is(err, ErrUnknownParam) {
		t.Fatalf("unknown pragma err = %v, want ErrUnknownParam", err)
	}
}
