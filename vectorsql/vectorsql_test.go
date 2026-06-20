package vectorsql

import (
	"testing"

	"github.com/tamnd/vec/catalog"
	"github.com/tamnd/vec/storage"
)

// parseStmt is a test helper that parses one statement or fails the test.
func parseStmt(t *testing.T, src string) Stmt {
	t.Helper()
	st, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", src, err)
	}
	return st
}

func TestParseSelectKNN(t *testing.T) {
	st := parseStmt(t, "SELECT id, title FROM docs WHERE genre = 'sci-fi' ORDER BY embedding <-> '[1, 2, 3]' LIMIT 5")
	sel, ok := st.(*SelectStmt)
	if !ok {
		t.Fatalf("got %T, want *SelectStmt", st)
	}
	if sel.From != "docs" {
		t.Errorf("From = %q, want docs", sel.From)
	}
	if len(sel.OrderBy) != 1 {
		t.Fatalf("OrderBy len = %d, want 1", len(sel.OrderBy))
	}
	de, ok := sel.OrderBy[0].Expr.(*DistanceExpr)
	if !ok {
		t.Fatalf("order expr %T, want *DistanceExpr", sel.OrderBy[0].Expr)
	}
	if de.Op != OpL2Distance {
		t.Errorf("op = %v, want L2", de.Op)
	}
	if lit, _ := constInt(sel.Limit); lit != 5 {
		t.Errorf("LIMIT = %d, want 5", lit)
	}
}

func TestParseDistanceOperators(t *testing.T) {
	cases := []struct {
		op   string
		want DistanceOp
	}{
		{"<->", OpL2Distance},
		{"<=>", OpCosineDistance},
		{"<#>", OpNegInnerProd},
		{"<+>", OpL1Distance},
	}
	for _, c := range cases {
		st := parseStmt(t, "SELECT id FROM t ORDER BY v "+c.op+" '[1,2]' LIMIT 1")
		de := st.(*SelectStmt).OrderBy[0].Expr.(*DistanceExpr)
		if de.Op != c.want {
			t.Errorf("%s parsed as %v, want %v", c.op, de.Op, c.want)
		}
	}
}

func TestParseErrorOffset(t *testing.T) {
	_, err := Parse("SELECT FROM")
	if err == nil {
		t.Fatal("expected parse error")
	}
	ve, ok := err.(*VecError)
	if !ok {
		t.Fatalf("got %T, want *VecError", err)
	}
	if ve.Numeric != 1000 {
		t.Errorf("numeric = %d, want 1000 (E_PARSE)", ve.Numeric)
	}
}

func TestParseRejectsJoin(t *testing.T) {
	_, err := Parse("SELECT id FROM a JOIN b ON a.x = b.x")
	ve, ok := err.(*VecError)
	if !ok {
		t.Fatalf("got %T (%v), want *VecError", err, err)
	}
	if ve.Numeric != 1001 {
		t.Errorf("numeric = %d, want 1001 (E_UNSUPPORTED)", ve.Numeric)
	}
}

func TestParseVectorLiteral(t *testing.T) {
	v, err := ParseVectorLiteral("[1.5, -2, 3.25]")
	if err != nil {
		t.Fatalf("ParseVectorLiteral error: %v", err)
	}
	want := []float32{1.5, -2, 3.25}
	if len(v) != len(want) {
		t.Fatalf("len = %d, want %d", len(v), len(want))
	}
	for i := range want {
		if v[i] != want[i] {
			t.Errorf("v[%d] = %v, want %v", i, v[i], want[i])
		}
	}
	if _, err := ParseVectorLiteral("[]"); err == nil {
		t.Error("empty vector should error")
	}
	if _, err := ParseVectorLiteral("1, 2, 3"); err == nil {
		t.Error("unbracketed vector should error")
	}
}

func TestBindCreateTable(t *testing.T) {
	st := parseStmt(t, "CREATE TABLE docs (id BIGINT PRIMARY KEY, title TEXT, year INT, embedding VECTOR(3))")
	s, err := BindCreateTable(st.(*CreateTableStmt))
	if err != nil {
		t.Fatalf("BindCreateTable error: %v", err)
	}
	if s.Name != "docs" {
		t.Errorf("name = %q, want docs", s.Name)
	}
	if s.IDName != "id" || s.IDKind != catalog.IDBigInt {
		t.Errorf("id = %q kind %v, want id/BigInt", s.IDName, s.IDKind)
	}
	vs := s.VectorColumns()
	if len(vs) != 1 || vs[0].Name != "embedding" || vs[0].Dim != 3 {
		t.Fatalf("vector columns = %+v, want one embedding dim 3", vs)
	}
	if len(s.MetadataColumns()) != 2 {
		t.Errorf("metadata columns = %d, want 2", len(s.MetadataColumns()))
	}
}

func TestBindCreateTableBadVector(t *testing.T) {
	st := parseStmt(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VECTOR)")
	_, err := BindCreateTable(st.(*CreateTableStmt))
	ve, ok := err.(*VecError)
	if !ok || ve.Numeric != 1004 {
		t.Fatalf("err = %v, want E_DIM (1004)", err)
	}
}

// testCollection builds a docs collection for bind tests.
func testCollection(t *testing.T) *catalog.Collection {
	t.Helper()
	cat := catalog.New()
	s := &catalog.Schema{
		Name:          "docs",
		IDName:        "id",
		IDKind:        catalog.IDBigInt,
		AutoIncrement: true,
		Columns: []catalog.ColumnDef{
			{Name: "embedding", Kind: catalog.ColumnVector, Dim: 3, ElemType: catalog.ElemFP32, VecMetric: catalog.MetricL2},
			{Name: "genre", Kind: catalog.ColumnMetadata, DataType: catalog.KindText, Nullable: true},
			{Name: "year", Kind: catalog.ColumnMetadata, DataType: catalog.KindBigInt, Nullable: true},
		},
	}
	coll, _, err := cat.CreateCollection(s, false)
	if err != nil {
		t.Fatalf("CreateCollection error: %v", err)
	}
	return coll
}

func TestBindSelectKNN(t *testing.T) {
	coll := testCollection(t)
	st := parseStmt(t, "SELECT genre FROM docs WHERE year >= 2000 ORDER BY embedding <-> '[1,2,3]' LIMIT 7")
	bs, err := BindSelect(st.(*SelectStmt), coll)
	if err != nil {
		t.Fatalf("BindSelect error: %v", err)
	}
	if !bs.IsKNN {
		t.Fatal("expected kNN")
	}
	if bs.BoundQuery.K != 7 {
		t.Errorf("K = %d, want 7", bs.BoundQuery.K)
	}
	if len(bs.BoundQuery.Vector) != 3 {
		t.Errorf("vector len = %d, want 3", len(bs.BoundQuery.Vector))
	}
	if bs.BoundQuery.Predicate == nil {
		t.Fatal("expected a predicate from WHERE")
	}
	cmp, ok := bs.BoundQuery.Predicate.(storage.Compare)
	if !ok {
		t.Fatalf("predicate %T, want storage.Compare", bs.BoundQuery.Predicate)
	}
	if cmp.Op != storage.OpGe {
		t.Errorf("op = %v, want OpGe", cmp.Op)
	}
}

func TestBindSelectVectorParam(t *testing.T) {
	coll := testCollection(t)
	st := parseStmt(t, "SELECT genre FROM docs ORDER BY embedding <-> :q LIMIT 3")
	bs, err := BindSelect(st.(*SelectStmt), coll)
	if err != nil {
		t.Fatalf("BindSelect error: %v", err)
	}
	if !bs.HasParam || bs.VectorParam.Name != "q" {
		t.Errorf("expected vector param :q, got %+v", bs.VectorParam)
	}
	if bs.BoundQuery.Vector != nil {
		t.Errorf("vector should be nil for a param query, got %v", bs.BoundQuery.Vector)
	}
}

func TestBindSelectDimMismatch(t *testing.T) {
	coll := testCollection(t)
	st := parseStmt(t, "SELECT genre FROM docs ORDER BY embedding <-> '[1,2]' LIMIT 3")
	_, err := BindSelect(st.(*SelectStmt), coll)
	ve, ok := err.(*VecError)
	if !ok || ve.Numeric != 1004 {
		t.Fatalf("err = %v, want E_DIM (1004)", err)
	}
}

func TestBindSelectNonKNN(t *testing.T) {
	coll := testCollection(t)
	st := parseStmt(t, "SELECT genre FROM docs WHERE year = 2000")
	_, err := BindSelect(st.(*SelectStmt), coll)
	ve, ok := err.(*VecError)
	if !ok || ve.Numeric != 1001 {
		t.Fatalf("err = %v, want E_UNSUPPORTED (1001)", err)
	}
}

func TestBindPredicateBoolChain(t *testing.T) {
	coll := testCollection(t)
	st := parseStmt(t, "SELECT genre FROM docs WHERE year >= 2000 AND genre = 'sci-fi' ORDER BY embedding <-> '[1,2,3]' LIMIT 5")
	bs, err := BindSelect(st.(*SelectStmt), coll)
	if err != nil {
		t.Fatalf("BindSelect error: %v", err)
	}
	and, ok := bs.BoundQuery.Predicate.(storage.And)
	if !ok {
		t.Fatalf("predicate %T, want storage.And", bs.BoundQuery.Predicate)
	}
	if len(and.Terms) != 2 {
		t.Errorf("and terms = %d, want 2", len(and.Terms))
	}
}

func TestBindPredicateUnknownColumn(t *testing.T) {
	coll := testCollection(t)
	st := parseStmt(t, "SELECT genre FROM docs WHERE rating > 4 ORDER BY embedding <-> '[1,2,3]' LIMIT 5")
	_, err := BindSelect(st.(*SelectStmt), coll)
	ve, ok := err.(*VecError)
	if !ok || ve.Numeric != 1002 {
		t.Fatalf("err = %v, want E_IDENT (1002)", err)
	}
}

func TestBindPredicateFlippedLiteral(t *testing.T) {
	coll := testCollection(t)
	st := parseStmt(t, "SELECT genre FROM docs WHERE 2000 < year ORDER BY embedding <-> '[1,2,3]' LIMIT 5")
	bs, err := BindSelect(st.(*SelectStmt), coll)
	if err != nil {
		t.Fatalf("BindSelect error: %v", err)
	}
	cmp := bs.BoundQuery.Predicate.(storage.Compare)
	if cmp.Op != storage.OpGt {
		t.Errorf("op = %v, want OpGt (flipped from <)", cmp.Op)
	}
	if cmp.Lit.I != 2000 {
		t.Errorf("lit = %v, want 2000", cmp.Lit.I)
	}
}
