package bulk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	vec "github.com/tamnd/vec"
)

// newDB opens an in-memory database with a 4-dim L2 vector column "emb" and a text
// column "title", matching the dump/load round-trip the tests exercise.
func newDB(t *testing.T) *vec.DB {
	t.Helper()
	db, err := vec.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := vec.CollectionSchema{
		Name: "docs",
		Columns: []vec.ColumnDef{
			{Name: "emb", Type: vec.TypeVector, Dim: 4, Metric: vec.MetricCosine},
			{Name: "title", Type: vec.TypeText},
		},
	}
	if err := db.CreateCollection(context.Background(), schema); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	return db
}

const sampleJSONL = `{"id":1,"emb":[1,0,0,0],"title":"one"}
{"id":2,"emb":[0,1,0,0],"title":"two"}
{"id":3,"emb":[0,0,1,0],"title":"three"}
`

func TestImportJSONL(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	stats, err := Import(ctx, db, "docs", FormatJSONL, strings.NewReader(sampleJSONL), ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if stats.RowsImported != 3 {
		t.Fatalf("RowsImported = %d, want 3", stats.RowsImported)
	}
	coll, err := db.Collection("docs")
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	n, err := coll.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Fatalf("Count = %d, want 3", n)
	}
}

func TestExportJSONLRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	if _, err := Import(ctx, db, "docs", FormatJSONL, strings.NewReader(sampleJSONL), ImportOptions{}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var buf bytes.Buffer
	if err := Export(ctx, db, "docs", &buf, ExportOptions{Format: ExportJSONL, IncludeVectors: true}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	got := map[uint64]string{}
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var row struct {
			ID    uint64    `json:"id"`
			Emb   []float64 `json:"emb"`
			Title string    `json:"title"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		if len(row.Emb) != 4 {
			t.Fatalf("id %d: emb dim = %d, want 4", row.ID, len(row.Emb))
		}
		got[row.ID] = row.Title
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	for id, title := range map[uint64]string{1: "one", 2: "two", 3: "three"} {
		if got[id] != title {
			t.Errorf("id %d title = %q, want %q", id, got[id], title)
		}
	}
}

func TestDumpLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newDB(t)
	if _, err := Import(ctx, src, "docs", FormatJSONL, strings.NewReader(sampleJSONL), ImportOptions{}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	var dump bytes.Buffer
	if err := Dump(ctx, src, &dump, DumpOptions{}); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	if !strings.Contains(dump.String(), "CREATE TABLE") {
		t.Fatalf("dump missing CREATE TABLE:\n%s", dump.String())
	}
	// The metric must ride on the index opclass, not the column type.
	if !strings.Contains(dump.String(), "vector_cosine_ops") {
		t.Fatalf("dump missing cosine opclass:\n%s", dump.String())
	}

	dst, err := vec.Open(":memory:")
	if err != nil {
		t.Fatalf("Open dst: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	loadStats, err := Load(ctx, dst, bytes.NewReader(dump.Bytes()), LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loadStats.PointsLoaded != 3 {
		t.Fatalf("PointsLoaded = %d, want 3", loadStats.PointsLoaded)
	}

	info, err := dst.GetCollection(ctx, "docs")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got := vectorMetric(info); got != vec.MetricCosine {
		t.Fatalf("loaded metric = %v, want cosine", got)
	}
	coll, err := dst.Collection("docs")
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	n, err := coll.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Fatalf("loaded Count = %d, want 3", n)
	}
}

func TestImportCSV(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	csv := "id,emb,title\n1,\"[1,0,0,0]\",one\n2,\"[0,1,0,0]\",two\n"
	stats, err := Import(ctx, db, "docs", FormatCSV, strings.NewReader(csv), ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if stats.RowsImported != 2 {
		t.Fatalf("RowsImported = %d, want 2", stats.RowsImported)
	}
}
