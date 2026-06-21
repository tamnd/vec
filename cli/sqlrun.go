package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tamnd/vec"
	"github.com/tamnd/vec/vectorsql"
)

// stmtResult is what one statement produced: either a result table (SELECT) or a
// status line (DDL and DML), never both.
type stmtResult struct {
	tbl    *table
	status string
}

// runStatement parses one statement and runs it against db. SELECT goes through the
// library query path; everything else is translated to the typed collection API,
// since the embedded engine drives DDL and DML through that API rather than SQL.
func runStatement(ctx context.Context, db *vec.DB, raw string) (stmtResult, error) {
	stmt, err := vectorsql.Parse(raw)
	if err != nil {
		return stmtResult{}, err
	}
	switch s := stmt.(type) {
	case *vectorsql.SelectStmt:
		return runSelect(ctx, db, raw)
	case *vectorsql.CreateTableStmt:
		return runCreateTable(ctx, db, s)
	case *vectorsql.CreateIndexStmt:
		return runCreateIndex(ctx, db, s)
	case *vectorsql.InsertStmt:
		return runInsert(ctx, db, s)
	case *vectorsql.DeleteStmt:
		return runDelete(ctx, db, s)
	case *vectorsql.DropStmt:
		return runDrop(ctx, db, s)
	case *vectorsql.PragmaStmt:
		return runPragma(ctx, db, s)
	default:
		return stmtResult{}, fmt.Errorf("statement type %T is not supported in this build", stmt)
	}
}

// runSelect runs a kNN SELECT through the library and renders the cursor.
func runSelect(ctx context.Context, db *vec.DB, raw string) (stmtResult, error) {
	rows, err := db.Exec(ctx, raw)
	if err != nil {
		return stmtResult{}, err
	}
	defer func() { _ = rows.Close() }()

	var results []vec.Result
	for rows.Next() {
		results = append(results, rows.Result())
	}
	if err := rows.Err(); err != nil {
		return stmtResult{}, err
	}
	return stmtResult{tbl: resultTable(results)}, nil
}

// resultTable turns query results into a render table: id first, the metadata
// columns in a stable order, then the distance.
func resultTable(results []vec.Result) *table {
	metaSet := map[string]struct{}{}
	for _, r := range results {
		for name := range r.Meta() {
			metaSet[name] = struct{}{}
		}
	}
	metaCols := make([]string, 0, len(metaSet))
	for name := range metaSet {
		metaCols = append(metaCols, name)
	}
	sort.Strings(metaCols)

	t := &table{cols: append(append([]string{"id"}, metaCols...), "distance")}
	for _, r := range results {
		row := make([]string, 0, len(t.cols))
		row = append(row, strconv.FormatUint(r.ID.N, 10))
		for _, name := range metaCols {
			if v, ok := r.Column(name); ok {
				row = append(row, v.String())
			} else {
				row = append(row, "")
			}
		}
		row = append(row, formatFloat(r.Distance))
		t.rows = append(t.rows, row)
	}
	return t
}

// runCreateTable translates CREATE TABLE into CreateCollection.
func runCreateTable(ctx context.Context, db *vec.DB, s *vectorsql.CreateTableStmt) (stmtResult, error) {
	schema := vec.CollectionSchema{Name: s.Name}
	for _, c := range s.Columns {
		def, err := columnDef(c)
		if err != nil {
			return stmtResult{}, err
		}
		schema.Columns = append(schema.Columns, def)
	}
	if err := db.CreateCollection(ctx, schema); err != nil {
		if s.IfNotExists && isAlreadyExists(err) {
			return stmtResult{status: "CREATE TABLE (exists)"}, nil
		}
		return stmtResult{}, err
	}
	return stmtResult{status: "CREATE TABLE"}, nil
}

// columnDef lowers one parsed column into a library column definition.
func columnDef(c vectorsql.ColumnSpec) (vec.ColumnDef, error) {
	if c.Type == nil {
		return vec.ColumnDef{}, fmt.Errorf("column %q has no type", c.Name)
	}
	out := vec.ColumnDef{Name: c.Name, NotNull: c.NotNull}
	switch strings.ToLower(c.Type.Name) {
	case "vector", "vec", "halfvec":
		out.Type = vec.TypeVector
		out.Dim = c.Type.Arg
		out.Metric = vec.MetricCosine // fixed at create; CREATE INDEX opclass is advisory
	case "bigint", "int8", "int", "integer", "int4", "smallint", "serial", "bigserial":
		out.Type = vec.TypeInt64
	case "double", "double precision", "float8", "float", "real", "float4", "numeric", "decimal":
		out.Type = vec.TypeFloat64
	case "bool", "boolean":
		out.Type = vec.TypeBool
	case "text", "varchar", "char", "character", "character varying":
		out.Type = vec.TypeText
	case "blob", "bytea", "bytes":
		out.Type = vec.TypeBytes
	case "json", "jsonb":
		out.Type = vec.TypeJSON
	case "timestamp", "timestamptz", "timestamp with time zone", "datetime":
		out.Type = vec.TypeTimestamp
	default:
		return vec.ColumnDef{}, fmt.Errorf("column %q: unsupported type %q", c.Name, c.Type.Name)
	}
	if c.Default != nil {
		v, err := literalValue(c.Default)
		if err != nil {
			return vec.ColumnDef{}, fmt.Errorf("column %q default: %w", c.Name, err)
		}
		out.Default = &v
	}
	return out, nil
}

// runCreateIndex translates CREATE INDEX into CreateIndex.
func runCreateIndex(ctx context.Context, db *vec.DB, s *vectorsql.CreateIndexStmt) (stmtResult, error) {
	it, err := indexType(s.IndexType)
	if err != nil {
		return stmtResult{}, err
	}
	params := vec.IndexParams{}
	for k, expr := range s.Options {
		n, err := literalInt(expr)
		if err != nil {
			return stmtResult{}, fmt.Errorf("index option %q: %w", k, err)
		}
		params[strings.ToLower(k)] = int(n)
	}
	name := s.Name
	if name == "" {
		name = s.Table + "_" + s.Column + "_idx"
	}
	spec := vec.IndexSpec{Name: name, Column: s.Column, Type: it, Params: params}
	if err := db.CreateIndex(ctx, s.Table, spec); err != nil {
		return stmtResult{}, err
	}
	return stmtResult{status: "CREATE INDEX"}, nil
}

// indexType maps the parsed index keyword to the library enum.
func indexType(name string) (vec.IndexType, error) {
	switch strings.ToLower(name) {
	case "hnsw":
		return vec.IndexHNSW, nil
	case "ivfflat":
		return vec.IndexIVFFlat, nil
	case "ivfpq":
		return vec.IndexIVFPQ, nil
	case "flat", "":
		return vec.IndexFlat, nil
	case "diskann":
		return vec.IndexDiskANN, nil
	default:
		return 0, fmt.Errorf("unknown index type %q", name)
	}
}

// runInsert translates INSERT into one upsert per row inside a single transaction.
func runInsert(ctx context.Context, db *vec.DB, s *vectorsql.InsertStmt) (stmtResult, error) {
	coll, err := db.Collection(s.Table)
	if err != nil {
		return stmtResult{}, err
	}
	schema, err := coll.Schema(ctx)
	if err != nil {
		return stmtResult{}, err
	}
	byName := map[string]vec.ColumnDef{}
	for _, c := range schema.Columns {
		byName[c.Name] = c
	}
	if len(s.Columns) == 0 {
		return stmtResult{}, fmt.Errorf("INSERT needs an explicit column list in this build")
	}

	points := make([]vec.Point, 0, len(s.Rows))
	for _, row := range s.Rows {
		if len(row) != len(s.Columns) {
			return stmtResult{}, fmt.Errorf("INSERT row has %d values for %d columns", len(row), len(s.Columns))
		}
		p := vec.Point{Vectors: map[string]vec.AnyVector{}, Meta: map[string]vec.Value{}}
		for i, name := range s.Columns {
			expr := row[i]
			switch {
			case name == "id" || name == "point_id":
				n, err := literalInt(expr)
				if err != nil {
					return stmtResult{}, fmt.Errorf("id value: %w", err)
				}
				p.ID = vec.IntID(uint64(n))
			case byName[name].Type == vec.TypeVector:
				sl, ok := expr.(*vectorsql.StringLit)
				if !ok {
					return stmtResult{}, fmt.Errorf("column %q expects a vector literal", name)
				}
				f, err := vectorsql.ParseVectorLiteral(sl.Value)
				if err != nil {
					return stmtResult{}, err
				}
				p.Vectors[name] = vec.AnyVector{Dense: vec.FromSlice32(f)}
			default:
				v, err := literalValue(expr)
				if err != nil {
					return stmtResult{}, fmt.Errorf("column %q: %w", name, err)
				}
				p.Meta[name] = v
			}
		}
		points = append(points, p)
	}

	if _, err := coll.UpsertBatch(ctx, points); err != nil {
		return stmtResult{}, err
	}
	return stmtResult{status: fmt.Sprintf("INSERT %d", len(points))}, nil
}

// runDelete supports DELETE ... WHERE id = N. Broader predicates are not lowered to
// deletes in this build.
func runDelete(ctx context.Context, db *vec.DB, s *vectorsql.DeleteStmt) (stmtResult, error) {
	id, ok := equalsIDPredicate(s.Where)
	if !ok {
		return stmtResult{}, fmt.Errorf("DELETE supports only WHERE id = N in this build")
	}
	coll, err := db.Collection(s.Table)
	if err != nil {
		return stmtResult{}, err
	}
	if err := coll.DeleteBatch(ctx, []vec.PointID{vec.IntID(id)}); err != nil {
		return stmtResult{}, err
	}
	return stmtResult{status: "DELETE 1"}, nil
}

// equalsIDPredicate matches `id = <int>` and returns the id.
func equalsIDPredicate(where vectorsql.Expr) (uint64, bool) {
	be, ok := where.(*vectorsql.BinaryExpr)
	if !ok || be.Op != "=" {
		return 0, false
	}
	col, ok := be.Left.(*vectorsql.ColumnRef)
	if !ok || (col.Name != "id" && col.Name != "point_id") {
		return 0, false
	}
	n, err := literalInt(be.Right)
	if err != nil {
		return 0, false
	}
	return uint64(n), true
}

// runDrop translates DROP TABLE and DROP INDEX.
func runDrop(ctx context.Context, db *vec.DB, s *vectorsql.DropStmt) (stmtResult, error) {
	if s.Index {
		table := s.OnTable
		if table == "" {
			return stmtResult{}, fmt.Errorf("DROP INDEX needs ON <table> in this build")
		}
		if err := db.DropIndex(ctx, table, s.Name); err != nil {
			if s.IfExists && isNotFound(err) {
				return stmtResult{status: "DROP INDEX (absent)"}, nil
			}
			return stmtResult{}, err
		}
		return stmtResult{status: "DROP INDEX"}, nil
	}
	if err := db.DropCollection(ctx, s.Name); err != nil {
		if s.IfExists && isNotFound(err) {
			return stmtResult{status: "DROP TABLE (absent)"}, nil
		}
		return stmtResult{}, err
	}
	return stmtResult{status: "DROP TABLE"}, nil
}

// runPragma reads or sets a pragma and prints the resulting value.
func runPragma(ctx context.Context, db *vec.DB, s *vectorsql.PragmaStmt) (stmtResult, error) {
	value := ""
	if s.Value != nil {
		v, err := literalValue(s.Value)
		if err != nil {
			return stmtResult{}, err
		}
		value = v.String()
	}
	got, err := db.Pragma(ctx, s.Name, value)
	if err != nil {
		return stmtResult{}, err
	}
	return stmtResult{tbl: &table{cols: []string{s.Name}, rows: [][]string{{got}}}}, nil
}

// literalValue evaluates a literal expression into a metadata value.
func literalValue(expr vectorsql.Expr) (vec.Value, error) {
	switch e := expr.(type) {
	case *vectorsql.IntLit:
		return vec.IntValue(e.Value), nil
	case *vectorsql.FloatLit:
		return vec.FloatValue(e.Value), nil
	case *vectorsql.StringLit:
		return vec.TextValue(e.Value), nil
	case *vectorsql.BoolLit:
		return vec.BoolValue(e.Value), nil
	case *vectorsql.NullLit:
		return vec.NullValue(), nil
	case *vectorsql.UnaryExpr:
		if e.Op == "-" {
			switch inner := e.Expr.(type) {
			case *vectorsql.IntLit:
				return vec.IntValue(-inner.Value), nil
			case *vectorsql.FloatLit:
				return vec.FloatValue(-inner.Value), nil
			}
		}
		return vec.Value{}, fmt.Errorf("unsupported literal %T", expr)
	default:
		return vec.Value{}, fmt.Errorf("unsupported literal %T", expr)
	}
}

// literalInt evaluates an expression expected to be an integer literal.
func literalInt(expr vectorsql.Expr) (int64, error) {
	switch e := expr.(type) {
	case *vectorsql.IntLit:
		return e.Value, nil
	case *vectorsql.FloatLit:
		return int64(e.Value), nil
	case *vectorsql.UnaryExpr:
		if e.Op == "-" {
			n, err := literalInt(e.Expr)
			return -n, err
		}
	}
	return 0, fmt.Errorf("expected an integer literal, got %T", expr)
}
