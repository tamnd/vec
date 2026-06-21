// Package vec is the embedded library API for the vec vector database (spec 14).
//
// vec stores vectors, ANN indexes, scalar metadata columns, the catalog, and free
// space in one self-describing file with the look and feel of SQLite. This root
// package is the public facade: it wires the storage engine, catalog, query
// planner and executor, ANN index SPI, and VectorSQL binder behind a small,
// goroutine-safe surface centered on *DB, *Collection, *Txn, and *QueryBuilder.
//
// The typical lifecycle is open a database, create a collection, upsert points,
// build an index, and run filtered ANN queries:
//
//	db, err := vec.Open("articles.vec")
//	if err != nil { log.Fatal(err) }
//	defer db.Close()
//
//	db.CreateCollection(ctx, vec.CollectionSchema{
//	    Name: "articles",
//	    Columns: []vec.ColumnDef{
//	        {Name: "embedding", Type: vec.TypeVector, Dim: 768, Metric: vec.MetricCosine},
//	        {Name: "author", Type: vec.TypeText},
//	    },
//	})
//	coll, _ := db.Collection("articles")
//	coll.UpsertBatch(ctx, points)
//	db.BuildIndex(ctx, "articles", "hnsw_embedding", vec.IndexParams{"m": 32})
//	rows, _ := coll.Query("embedding", q).K(10).Filter("author = ?", "alice").Exec(ctx)
//
// A *DB is safe for concurrent use; a *Txn and a *Rows are owned by one goroutine
// from creation to close (spec 14 §11).
package vec

// version, commit, and date carry build information. The release build sets them
// through -ldflags -X at link time (see .goreleaser.yaml); a plain `go build`
// leaves the defaults, so a source build reports the in-tree version with no
// commit or date.
var (
	version = "0.1.0"
	commit  = ""
	date    = ""
)

// Version returns the vec library version string (semver).
func Version() string { return version }

// BuildInfo returns the version, commit, and build date. The commit and date are
// empty for a build that was not made by the release pipeline.
func BuildInfo() (ver, commitHash, buildDate string) { return version, commit, date }
