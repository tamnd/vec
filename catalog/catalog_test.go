package catalog

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/tamnd/vector/storage"
)

// docsSchema is the worked-example documents collection (spec 02 §12.1) trimmed
// to the columns the catalog layer exercises.
func docsSchema() *Schema {
	return &Schema{
		Name:          "documents",
		IDKind:        IDBigInt,
		AutoIncrement: true,
		Columns: []ColumnDef{
			{Name: "embedding", Kind: ColumnVector, Dim: 768}, // metric defaults to cosine
			{Name: "title", Kind: ColumnMetadata, DataType: KindText},
			{Name: "author", Kind: ColumnMetadata, DataType: KindText, Nullable: true},
			{Name: "published", Kind: ColumnMetadata, DataType: KindTimestamp, Nullable: true},
			{Name: "word_count", Kind: ColumnMetadata, DataType: KindInt, Default: ptr(Int(0))},
			{Name: "lang", Kind: ColumnMetadata, DataType: KindText, Default: ptr(Text("en"))},
		},
	}
}

func ptr(v Value) *Value { return &v }

func mustCreate(t *testing.T, cat *Catalog, s *Schema) *Collection {
	t.Helper()
	c, created, err := cat.CreateCollection(s, false)
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if !created {
		t.Fatal("expected created=true")
	}
	return c
}

func TestCreateCollectionDefaultsAndLowering(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())

	emb := c.Schema.Column("embedding")
	if emb.ElemType != ElemFP32 {
		t.Errorf("element type default = %s, want fp32", emb.ElemType)
	}
	if emb.VecMetric != MetricCosine {
		t.Errorf("metric default = %s, want cosine", emb.VecMetric)
	}
	if c.Schema.IDName != "id" {
		t.Errorf("id name = %q, want id", c.Schema.IDName)
	}

	def := c.StorageDef()
	if def.Dims != 768 || def.Elem != storage.ElemFP32 {
		t.Errorf("lowered def dims=%d elem=%d", def.Dims, def.Elem)
	}
	if def.Metric.String() != "cosine" {
		t.Errorf("lowered metric = %v, want cosine", def.Metric)
	}
	if len(def.Columns) != 5 {
		t.Fatalf("lowered metadata columns = %d, want 5", len(def.Columns))
	}
	if _, ok := c.ColID("title"); !ok {
		t.Error("title has no engine ColID")
	}
	if _, ok := c.ColID("embedding"); ok {
		t.Error("vector column should not have a metadata ColID")
	}
}

func TestReservedNameRejected(t *testing.T) {
	cat := New()
	_, _, err := cat.CreateCollection(&Schema{
		Name:    "vec_internal",
		IDKind:  IDBigInt,
		Columns: []ColumnDef{{Name: "v", Kind: ColumnVector, Dim: 4}},
	}, false)
	if !errors.Is(err, ErrReservedName) {
		t.Fatalf("err = %v, want ErrReservedName", err)
	}
}

func TestSchemaWithoutVectorRejected(t *testing.T) {
	cat := New()
	_, _, err := cat.CreateCollection(&Schema{
		Name:    "novec",
		IDKind:  IDBigInt,
		Columns: []ColumnDef{{Name: "x", Kind: ColumnMetadata, DataType: KindInt}},
	}, false)
	if !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("err = %v, want ErrInvalidSchema", err)
	}
}

func TestDuplicateColumnRejected(t *testing.T) {
	cat := New()
	_, _, err := cat.CreateCollection(&Schema{
		Name:   "dup",
		IDKind: IDBigInt,
		Columns: []ColumnDef{
			{Name: "v", Kind: ColumnVector, Dim: 4},
			{Name: "x", Kind: ColumnMetadata, DataType: KindInt},
			{Name: "x", Kind: ColumnMetadata, DataType: KindText},
		},
	}, false)
	if !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("err = %v, want ErrInvalidSchema", err)
	}
}

func TestBadDimensionRejected(t *testing.T) {
	cat := New()
	for _, dim := range []uint32{0, maxDim + 1} {
		_, _, err := cat.CreateCollection(&Schema{
			Name:    "d",
			IDKind:  IDBigInt,
			Columns: []ColumnDef{{Name: "v", Kind: ColumnVector, Dim: dim}},
		}, false)
		if !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("dim=%d err = %v, want ErrInvalidSchema", dim, err)
		}
	}
}

func TestMetricElementMismatchRejected(t *testing.T) {
	cat := New()
	_, _, err := cat.CreateCollection(&Schema{
		Name:   "m",
		IDKind: IDBigInt,
		Columns: []ColumnDef{
			{Name: "v", Kind: ColumnVector, Dim: 8, ElemType: ElemFP32, VecMetric: MetricHamming},
		},
	}, false)
	if !errors.Is(err, ErrMetricUnsupported) {
		t.Fatalf("err = %v, want ErrMetricUnsupported", err)
	}
}

func TestDefaultMetricPerElement(t *testing.T) {
	cases := []struct {
		elem ElementType
		want Metric
	}{
		{ElemFP32, MetricCosine},
		{ElemFP16, MetricCosine},
		{ElemInt8, MetricL2},
		{ElemBinary, MetricHamming},
	}
	for _, c := range cases {
		if got := DefaultMetric(c.elem); got != c.want {
			t.Errorf("DefaultMetric(%s) = %s, want %s", c.elem, got, c.want)
		}
	}
}

func TestIfNotExists(t *testing.T) {
	cat := New()
	mustCreate(t, cat, docsSchema())
	c2, created, err := cat.CreateCollection(docsSchema(), true)
	if err != nil || created || c2 == nil {
		t.Fatalf("IF NOT EXISTS: created=%v err=%v", created, err)
	}
	_, _, err = cat.CreateCollection(docsSchema(), false)
	if !errors.Is(err, ErrCollectionExists) {
		t.Fatalf("err = %v, want ErrCollectionExists", err)
	}
}

func TestAutoIncrementMonotonic(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	for want := uint64(0); want < 5; want++ {
		pid, _, err := c.PrepareID(nil)
		if err != nil {
			t.Fatalf("PrepareID: %v", err)
		}
		if pid.Kind != IDBigInt || pid.U != want {
			t.Fatalf("auto id = %v, want %d", pid, want)
		}
	}
}

func TestSuppliedIDReservesSequence(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	supplied := BigIntID(100)
	if _, _, err := c.PrepareID(&supplied); err != nil {
		t.Fatalf("PrepareID supplied: %v", err)
	}
	pid, _, err := c.PrepareID(nil)
	if err != nil {
		t.Fatalf("PrepareID auto: %v", err)
	}
	if pid.U != 101 {
		t.Fatalf("auto id after supplied 100 = %d, want 101", pid.U)
	}
}

func TestDeletedIDNotReused(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	supplied := BigIntID(7)
	if _, _, err := c.PrepareID(&supplied); err != nil {
		t.Fatalf("PrepareID: %v", err)
	}
	c.NoteDeleted(supplied)
	if _, _, err := c.PrepareID(&supplied); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("reuse err = %v, want ErrDuplicateKey", err)
	}
}

func TestIDFormMismatch(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	bad := TextID("abc")
	if _, _, err := c.PrepareID(&bad); !errors.Is(err, ErrIDTypeMismatch) {
		t.Fatalf("err = %v, want ErrIDTypeMismatch", err)
	}
}

func TestSequenceOverflow(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	c.id.seq = math.MaxUint64
	if _, err := c.NextID(); !errors.Is(err, ErrSequenceOverflow) {
		t.Fatalf("err = %v, want ErrSequenceOverflow", err)
	}
}

func TestValidateVectorChecks(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	good := make([]float32, 768)
	if err := c.ValidateVector("embedding", good); err != nil {
		t.Fatalf("good vector rejected: %v", err)
	}
	if err := c.ValidateVector("embedding", good[:10]); !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("short vector err = %v, want ErrDimMismatch", err)
	}
	nan := make([]float32, 768)
	nan[3] = float32(math.NaN())
	if err := c.ValidateVector("embedding", nan); !errors.Is(err, ErrNaNInVector) {
		t.Fatalf("nan err = %v, want ErrNaNInVector", err)
	}
	inf := make([]float32, 768)
	inf[0] = float32(math.Inf(1))
	if err := c.ValidateVector("embedding", inf); !errors.Is(err, ErrInfInVector) {
		t.Fatalf("inf err = %v, want ErrInfInVector", err)
	}
}

func TestInt8RangeCheck(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, &Schema{
		Name:    "q",
		IDKind:  IDBigInt,
		Columns: []ColumnDef{{Name: "v", Kind: ColumnVector, Dim: 4, ElemType: ElemInt8}},
	})
	if err := c.ValidateVector("v", []float32{1, 2, 200, 4}); !errors.Is(err, ErrValueOutOfRange) {
		t.Fatalf("err = %v, want ErrValueOutOfRange", err)
	}
	if err := c.ValidateVector("v", []float32{1, 2, 3, 4}); err != nil {
		t.Fatalf("in-range int8 rejected: %v", err)
	}
}

func TestLowerMetaDefaultsAndNotNull(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	now := time.Unix(1700000000, 0).UTC()

	// title is NOT NULL with no default: omitting it is a violation.
	if _, err := c.LowerMeta(map[string]Value{}, now); !errors.Is(err, ErrNullViolation) {
		t.Fatalf("missing NOT NULL err = %v, want ErrNullViolation", err)
	}

	row, err := c.LowerMeta(map[string]Value{
		"title": Text("Attention Is All You Need"),
	}, now)
	if err != nil {
		t.Fatalf("LowerMeta: %v", err)
	}
	// word_count and lang defaults are filled.
	wc, _ := c.ColID("word_count")
	if row[wc].Kind != storage.KindInt || row[wc].I != 0 {
		t.Errorf("word_count default = %+v, want int 0", row[wc])
	}
	lang, _ := c.ColID("lang")
	if row[lang].S != "en" {
		t.Errorf("lang default = %q, want en", row[lang].S)
	}
	// nullable author left absent lowers to NULL.
	author, _ := c.ColID("author")
	if !row[author].IsNull() {
		t.Errorf("absent nullable author = %+v, want NULL", row[author])
	}
}

func TestLowerMetaTypeMismatch(t *testing.T) {
	cat := New()
	c := mustCreate(t, cat, docsSchema())
	now := time.Unix(1700000000, 0).UTC()
	_, err := c.LowerMeta(map[string]Value{
		"title":      Text("x"),
		"word_count": Text("not a number"),
	}, now)
	if !errors.Is(err, ErrTypeMismatch) {
		t.Fatalf("err = %v, want ErrTypeMismatch", err)
	}
}

func TestSystemCollections(t *testing.T) {
	cat := New()
	cat.SetClock(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	mustCreate(t, cat, docsSchema())

	colls := cat.VecCollections()
	if len(colls) != 1 || colls[0]["name"] != "documents" || colls[0]["schema_mode"] != "fixed" {
		t.Fatalf("vec_collections = %+v", colls)
	}

	cols := cat.VecColumns()
	if len(cols) != 6 { // 1 vector + 5 metadata
		t.Fatalf("vec_columns rows = %d, want 6", len(cols))
	}
	var sawVector bool
	for _, r := range cols {
		if r["name"] == "embedding" {
			sawVector = true
			if r["kind"] != "vector" || r["dim"] != "768" || r["metric"] != "cosine" {
				t.Errorf("embedding row = %+v", r)
			}
		}
	}
	if !sawVector {
		t.Error("embedding column missing from vec_columns")
	}
}

func TestDropCollection(t *testing.T) {
	cat := New()
	mustCreate(t, cat, docsSchema())
	if _, err := cat.DropCollection("documents", false); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
	if _, err := cat.Get("documents"); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("err = %v, want ErrCollectionNotFound", err)
	}
	if _, err := cat.DropCollection("documents", true); err != nil {
		t.Fatalf("DROP IF EXISTS on absent: %v", err)
	}
	if _, err := cat.DropCollection("documents", false); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatalf("err = %v, want ErrCollectionNotFound", err)
	}
}

func TestValueThreeValuedLogic(t *testing.T) {
	// NULL comparisons are UNKNOWN (spec 02 §7.11).
	if _, known := Int(1).Equal(Null); known {
		t.Error("x = NULL should be UNKNOWN")
	}
	// NaN = NaN is NULL/UNKNOWN in three-valued logic (spec 02 §7.4).
	nan := Double(math.NaN())
	if _, known := nan.Equal(nan); known {
		t.Error("NaN = NaN should be UNKNOWN")
	}
	// Cross-kind numeric comparison.
	if eq, known := BigInt(5).Equal(Double(5.0)); !known || !eq {
		t.Errorf("5 = 5.0 = (%v,%v), want true,true", eq, known)
	}
	// Ordering: NaN sorts greatest.
	if Double(math.NaN()).Order(Double(1e300)) != 1 {
		t.Error("NaN should order after finite floats")
	}
	// Text code-point order.
	if Text("apple").Order(Text("banana")) != -1 {
		t.Error("apple < banana")
	}
}

func TestEngineRoundTripThroughCatalog(t *testing.T) {
	// The lowered def must produce a working engine collection and accept a row
	// built by LowerMeta, proving the catalog-to-storage seam (spec 02 §11).
	cat := New()
	c := mustCreate(t, cat, &Schema{
		Name:          "mini",
		IDKind:        IDBigInt,
		AutoIncrement: true,
		Columns: []ColumnDef{
			{Name: "v", Kind: ColumnVector, Dim: 4, ElemType: ElemFP32, VecMetric: MetricL2},
			{Name: "score", Kind: ColumnMetadata, DataType: KindDouble, Nullable: true},
		},
	})
	eng := storage.NewEngine()
	if err := eng.CreateCollection(c.StorageDef()); err != nil {
		t.Fatalf("engine CreateCollection: %v", err)
	}
	pid, eid, err := c.PrepareID(nil)
	if err != nil {
		t.Fatalf("PrepareID: %v", err)
	}
	if err := c.ValidateVector("v", []float32{1, 2, 3, 4}); err != nil {
		t.Fatalf("ValidateVector: %v", err)
	}
	row, err := c.LowerMeta(map[string]Value{"score": Double(0.5)}, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("LowerMeta: %v", err)
	}
	tx := eng.Begin(true)
	pos, err := eng.Insert(tx, c.ID, eid, []float32{1, 2, 3, 4}, row)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	snap := eng.Snapshot()
	rec, err := eng.Fetch(c.ID, pos, nil, snap)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if rec.Vec[0] != 1 || rec.Vec[3] != 4 {
		t.Errorf("fetched vec = %v", rec.Vec)
	}
	_ = pid
}
