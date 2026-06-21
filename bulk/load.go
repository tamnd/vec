package bulk

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"

	vec "github.com/tamnd/vec"
	"github.com/tamnd/vec/vectorsql"
)

// LoadOptions controls loading a logical dump (spec 17 §3.4).
type LoadOptions struct {
	// Upsert treats every inserted point as an upsert, overwriting on id
	// conflict. This is the migration path: dump from version N, load into N+1.
	// When false, a duplicate id surfaces the engine's already-exists error.
	Upsert bool
	// BatchSize bounds how many points are written per UpsertBatch (default 4096).
	BatchSize int
}

func (o LoadOptions) batchSize() int {
	if o.BatchSize > 0 {
		return o.BatchSize
	}
	return 4096
}

// LoadStats reports the outcome of a load (spec 17 §3.4).
type LoadStats struct {
	CollectionsCreated int
	IndexesCreated     int
	PointsLoaded       int64
}

// loadSchema is the column-type picture Load builds from a CREATE TABLE so it can
// coerce INSERT literals and find the id and vector columns.
type loadSchema struct {
	vectorColumn string
	dim          int
	idColumn     string
	colType      map[string]vec.ColumnType
}

// Load reads a logical VectorSQL dump from r and rebuilds it in db (spec 17 §3.4).
// CREATE TABLE, CREATE INDEX and INSERT statements are honored; transaction-control
// statements are accepted and ignored. A gzip-wrapped dump is detected and
// decompressed automatically. The metric of each vector column is recovered from
// the matching CREATE INDEX opclass, defaulting to L2 when a table has no index.
//
// The loader streams one statement at a time, so a multi-gigabyte dump loads in
// bounded memory. Collection creation is deferred until the metric is known: a
// CREATE TABLE is held pending, the matching CREATE INDEX creates the collection
// with its opclass metric, and a table with no index is created at L2 the moment
// its first INSERT arrives.
func Load(ctx context.Context, db *vec.DB, r io.Reader, opts LoadOptions) (LoadStats, error) {
	var stats LoadStats

	br := bufio.NewReader(r)
	if gzipMagic(br) {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return stats, fmt.Errorf("bulk: open gzip dump: %w", err)
		}
		defer func() { _ = gz.Close() }()
		br = bufio.NewReader(gz)
	}

	l := &loader{
		db:      db,
		opts:    opts,
		pending: map[string]*vectorsql.CreateTableStmt{},
		schemas: map[string]loadSchema{},
	}
	sc := newStmtScanner(br)
	for {
		raw, err := sc.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return l.stats, err
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		stmt, perr := vectorsql.Parse(raw)
		if perr != nil {
			return l.stats, fmt.Errorf("bulk: parse statement: %w\n%s", perr, snippet(raw))
		}
		if err := l.apply(ctx, stmt); err != nil {
			return l.stats, err
		}
	}
	return l.stats, nil
}

// loader holds the streaming load state.
type loader struct {
	db      *vec.DB
	opts    LoadOptions
	pending map[string]*vectorsql.CreateTableStmt
	schemas map[string]loadSchema
	stats   LoadStats
}

func (l *loader) apply(ctx context.Context, stmt vectorsql.Stmt) error {
	switch s := stmt.(type) {
	case *vectorsql.CreateTableStmt:
		l.pending[s.Name] = s
		return nil
	case *vectorsql.CreateIndexStmt:
		if _, err := l.ensureCreated(ctx, s.Table, metricFromOpclass(s.Opclass)); err != nil {
			return err
		}
		if err := createIndexFromStmt(ctx, l.db, s); err != nil {
			return err
		}
		l.stats.IndexesCreated++
		return nil
	case *vectorsql.InsertStmt:
		sch, err := l.ensureCreated(ctx, s.Table, vec.MetricL2)
		if err != nil {
			return err
		}
		n, err := loadInsert(ctx, l.db, s, sch, l.opts)
		if err != nil {
			return err
		}
		l.stats.PointsLoaded += n
		return nil
	default:
		// Transaction control and anything else is ignored on load.
		return nil
	}
}

// ensureCreated creates a pending collection if it has not been created yet,
// using metric for its vector column. A collection created by an earlier statement
// is returned as-is; metric only applies on first creation.
func (l *loader) ensureCreated(ctx context.Context, table string, metric vec.Metric) (loadSchema, error) {
	if sch, ok := l.schemas[table]; ok {
		return sch, nil
	}
	stmt, ok := l.pending[table]
	if !ok {
		return loadSchema{}, fmt.Errorf("bulk: statement references unknown table %q", table)
	}
	sch, err := createFromTable(ctx, l.db, stmt, metric)
	if err != nil {
		return loadSchema{}, err
	}
	l.schemas[table] = sch
	delete(l.pending, table)
	l.stats.CollectionsCreated++
	return sch, nil
}

// createFromTable builds a CollectionSchema from a CREATE TABLE and creates the
// collection, setting the vector column metric to the supplied value.
func createFromTable(ctx context.Context, db *vec.DB, s *vectorsql.CreateTableStmt, metric vec.Metric) (loadSchema, error) {
	sch := loadSchema{colType: map[string]vec.ColumnType{}}
	cs := vec.CollectionSchema{Name: s.Name}
	for _, col := range s.Columns {
		ct, dim, err := columnTypeFromTypeRef(col.Type)
		if err != nil {
			return sch, fmt.Errorf("bulk: table %q column %q: %w", s.Name, col.Name, err)
		}
		def := vec.ColumnDef{Name: col.Name, Type: ct, NotNull: col.NotNull}
		if ct == vec.TypeVector {
			def.Dim = dim
			def.Metric = metric
			sch.vectorColumn = col.Name
			sch.dim = dim
		}
		if ct == vec.TypeInt64 && (isPK(s, col) || col.Name == "id" || col.Name == "point_id") {
			sch.idColumn = col.Name
		}
		sch.colType[col.Name] = ct
		cs.Columns = append(cs.Columns, def)
	}
	if err := db.CreateCollection(ctx, cs); err != nil {
		return sch, err
	}
	return sch, nil
}

func isPK(s *vectorsql.CreateTableStmt, col vectorsql.ColumnSpec) bool {
	if col.PrimaryKey {
		return true
	}
	for _, name := range s.PrimaryKey {
		if name == col.Name {
			return true
		}
	}
	return false
}

func createIndexFromStmt(ctx context.Context, db *vec.DB, s *vectorsql.CreateIndexStmt) error {
	spec := vec.IndexSpec{
		Name:   s.Name,
		Column: s.Column,
		Type:   indexTypeFromName(s.IndexType),
		Params: indexParamsFromOptions(s.Options),
	}
	return db.CreateIndex(ctx, s.Table, spec)
}

// loadInsert evaluates an INSERT's rows into points and writes them in batches.
func loadInsert(ctx context.Context, db *vec.DB, s *vectorsql.InsertStmt, sch loadSchema, opts LoadOptions) (int64, error) {
	coll, err := db.Collection(s.Table)
	if err != nil {
		return 0, err
	}
	batchSize := opts.batchSize()
	batch := make([]vec.Point, 0, batchSize)
	var total int64

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := coll.UpsertBatch(ctx, batch); err != nil {
			return err
		}
		total += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for _, exprRow := range s.Rows {
		if len(exprRow) != len(s.Columns) {
			return total, fmt.Errorf("bulk: INSERT into %q: row has %d values for %d columns", s.Table, len(exprRow), len(s.Columns))
		}
		p, err := pointFromInsertRow(s.Columns, exprRow, sch)
		if err != nil {
			return total, err
		}
		batch = append(batch, p)
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

// pointFromInsertRow builds a Point from one INSERT row under the load schema.
func pointFromInsertRow(cols []string, exprs []vectorsql.Expr, sch loadSchema) (vec.Point, error) {
	p := vec.Point{Vectors: map[string]vec.AnyVector{}}
	for i, col := range cols {
		val, err := evalLiteral(exprs[i])
		if err != nil {
			return vec.Point{}, fmt.Errorf("column %q: %w", col, err)
		}
		switch col {
		case sch.vectorColumn:
			if val == nil {
				return vec.Point{}, fmt.Errorf("column %q: vector is null", col)
			}
			vecf, err := parseVectorField(val)
			if err != nil {
				return vec.Point{}, fmt.Errorf("column %q: %w", col, err)
			}
			p.Vectors[col] = vec.AnyVector{Dense: vec.FromSlice32(vecf)}
		case sch.idColumn:
			if val == nil {
				continue // engine assigns
			}
			id, err := toPointID(val)
			if err != nil {
				return vec.Point{}, fmt.Errorf("column %q: %w", col, err)
			}
			p.ID = id
		default:
			t, ok := sch.colType[col]
			if !ok {
				continue
			}
			if val == nil {
				if p.Meta == nil {
					p.Meta = map[string]vec.Value{}
				}
				p.Meta[col] = vec.NullValue()
				continue
			}
			v, err := valueFromAny(val, t)
			if err != nil {
				return vec.Point{}, fmt.Errorf("column %q: %w", col, err)
			}
			if p.Meta == nil {
				p.Meta = map[string]vec.Value{}
			}
			p.Meta[col] = v
		}
	}
	return p, nil
}

// evalLiteral evaluates a VectorSQL literal expression into a plain Go value. Only
// the literal forms a dump emits are supported; anything else is an error.
func evalLiteral(e vectorsql.Expr) (any, error) {
	switch x := e.(type) {
	case *vectorsql.IntLit:
		return x.Value, nil
	case *vectorsql.FloatLit:
		return x.Value, nil
	case *vectorsql.StringLit:
		return x.Value, nil
	case *vectorsql.BoolLit:
		return x.Value, nil
	case *vectorsql.NullLit:
		return nil, nil
	case *vectorsql.UnaryExpr:
		if x.Op == "-" {
			inner, err := evalLiteral(x.Expr)
			if err != nil {
				return nil, err
			}
			switch n := inner.(type) {
			case int64:
				return -n, nil
			case float64:
				return -n, nil
			}
		}
		return nil, fmt.Errorf("unsupported unary expression %q in dump", x.Op)
	default:
		return nil, fmt.Errorf("unsupported literal %T in dump", e)
	}
}

// columnTypeFromTypeRef maps a parsed VectorSQL type onto a facade ColumnType.
func columnTypeFromTypeRef(t *vectorsql.TypeRef) (vec.ColumnType, int, error) {
	if t == nil {
		return 0, 0, fmt.Errorf("missing type")
	}
	switch strings.ToLower(t.Name) {
	case "vector", "halfvec", "bit", "sparsevec":
		if !t.HasArg {
			return 0, 0, fmt.Errorf("vector type needs a dimension")
		}
		return vec.TypeVector, t.Arg, nil
	case "bigint", "int8", "int", "integer", "int4", "smallint", "int2", "serial", "bigserial":
		return vec.TypeInt64, 0, nil
	case "double precision", "double", "float", "float8", "real", "float4", "numeric", "decimal":
		return vec.TypeFloat64, 0, nil
	case "boolean", "bool":
		return vec.TypeBool, 0, nil
	case "text", "varchar", "char", "character", "string", "uuid":
		return vec.TypeText, 0, nil
	case "bytea", "blob", "bytes", "binary":
		return vec.TypeBytes, 0, nil
	case "json", "jsonb":
		return vec.TypeJSON, 0, nil
	case "timestamp", "timestamptz", "datetime", "date":
		return vec.TypeTimestamp, 0, nil
	default:
		return vec.TypeText, 0, nil
	}
}

func indexTypeFromName(name string) vec.IndexType {
	switch strings.ToLower(name) {
	case "hnsw":
		return vec.IndexHNSW
	case "ivfflat":
		return vec.IndexIVFFlat
	case "ivfpq":
		return vec.IndexIVFPQ
	case "diskann":
		return vec.IndexDiskANN
	default:
		return vec.IndexFlat
	}
}

// indexParamsFromOptions evaluates the WITH (...) options into IndexParams. The
// opclass is consumed separately (for the metric); these are the build knobs.
func indexParamsFromOptions(opts map[string]vectorsql.Expr) vec.IndexParams {
	if len(opts) == 0 {
		return nil
	}
	out := vec.IndexParams{}
	for k, e := range opts {
		v, err := evalLiteral(e)
		if err != nil {
			continue
		}
		out[strings.ToLower(k)] = v
	}
	return out
}

func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
