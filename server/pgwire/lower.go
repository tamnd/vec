package pgwire

import (
	"context"
	"errors"
	"strings"

	vec "github.com/tamnd/vec"
	"github.com/tamnd/vec/vectorsql"
)

// errIs is a thin alias over errors.Is for the IfExists/IfNotExists checks.
func errIs(err, target error) bool { return errors.Is(err, target) }

// execCreateTable lowers CREATE TABLE onto CreateCollection (spec 16 §4.5). The
// id/primary-key column folds into the implicit BIGINT key; a vector(N) column
// becomes the vector column; scalar columns become metadata columns.
func (c *Conn) execCreateTable(ctx context.Context, s *vectorsql.CreateTableStmt) *pgError {
	schema := vec.CollectionSchema{Name: s.Name}
	for _, col := range s.Columns {
		if col.PrimaryKey && isIDName(col.Name) {
			continue // implicit primary key
		}
		cd, perr := lowerColumn(col)
		if perr != nil {
			return perr
		}
		if cd == nil {
			continue
		}
		schema.Columns = append(schema.Columns, *cd)
	}
	if err := c.opts.DB.CreateCollection(ctx, schema); err != nil {
		if s.IfNotExists && isAlreadyExists(err) {
			_ = c.w.writeCommandComplete("CREATE TABLE")
			return nil
		}
		return c.mapError(err)
	}
	_ = c.w.writeCommandComplete("CREATE TABLE")
	return nil
}

// lowerColumn lowers one CREATE TABLE column. A nil result means the column is
// dropped (the implicit id key); a non-nil pgError reports an unsupported type.
func lowerColumn(col vectorsql.ColumnSpec) (*vec.ColumnDef, *pgError) {
	if isIDName(col.Name) && (col.PrimaryKey || col.Type == nil) {
		return nil, nil
	}
	tname := ""
	if col.Type != nil {
		tname = strings.ToLower(col.Type.Name)
	}
	cd := vec.ColumnDef{Name: col.Name, NotNull: col.NotNull}
	switch tname {
	case "vector", "halfvec", "floatvec":
		if col.Type == nil || !col.Type.HasArg || col.Type.Arg <= 0 {
			return nil, &pgError{code: "42601", message: "vector column needs a dimension"}
		}
		cd.Type = vec.TypeVector
		cd.Dim = col.Type.Arg
		cd.Metric = vec.MetricL2 // metric is fixed later by CREATE INDEX opclass
	case "bigint", "int8", "integer", "int", "int4", "smallint", "int2", "serial", "bigserial":
		cd.Type = vec.TypeInt64
	case "real", "float4", "double", "double precision", "float8", "numeric", "decimal":
		cd.Type = vec.TypeFloat64
	case "boolean", "bool":
		cd.Type = vec.TypeBool
	case "text", "varchar", "char", "character", "character varying", "name", "uuid":
		cd.Type = vec.TypeText
	case "bytea":
		cd.Type = vec.TypeBytes
	case "json", "jsonb":
		cd.Type = vec.TypeJSON
	case "timestamp", "timestamptz", "date":
		cd.Type = vec.TypeTimestamp
	default:
		return nil, unsupported("column type %q", tname)
	}
	return &cd, nil
}

// execCreateIndex lowers CREATE INDEX onto CreateIndex (spec 16 §4.5, §18.3). The
// opclass name maps to the metric; the WITH options map to index params.
func (c *Conn) execCreateIndex(ctx context.Context, s *vectorsql.CreateIndexStmt) *pgError {
	spec := vec.IndexSpec{
		Name:   s.Name,
		Column: s.Column,
		Type:   indexTypeFor(s.IndexType),
		Params: optionParams(s.Options),
	}
	if spec.Name == "" {
		spec.Name = s.Table + "_" + s.Column + "_" + strings.ToLower(s.IndexType)
	}
	if err := c.opts.DB.CreateIndex(ctx, s.Table, spec); err != nil {
		if s.IfNotExists && isAlreadyExists(err) {
			_ = c.w.writeCommandComplete("CREATE INDEX")
			return nil
		}
		return c.mapError(err)
	}
	_ = c.w.writeCommandComplete("CREATE INDEX")
	return nil
}

// indexTypeFor maps the SQL index type keyword to the vec index type.
func indexTypeFor(name string) vec.IndexType {
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

// optionParams lowers a WITH (k=v) option map to index params.
func optionParams(opts map[string]vectorsql.Expr) vec.IndexParams {
	if len(opts) == 0 {
		return nil
	}
	out := make(vec.IndexParams, len(opts))
	for k, e := range opts {
		if iv, ok := litInt(e); ok {
			out[strings.ToLower(k)] = int(iv)
			continue
		}
		if fv, ok := litFloat(e); ok {
			out[strings.ToLower(k)] = fv
			continue
		}
		if sv, ok := litString(e); ok {
			out[strings.ToLower(k)] = sv
		}
	}
	return out
}

// execInsert lowers INSERT onto UpsertBatch (spec 16 §4.5). Each VALUES row
// becomes one point; the vector column carries the dense vector, scalar columns
// the metadata.
func (c *Conn) execInsert(ctx context.Context, s *vectorsql.InsertStmt) *pgError {
	info, err := c.opts.DB.GetCollection(ctx, s.Table)
	if err != nil {
		return c.mapError(err)
	}
	vecCol := ""
	colType := make(map[string]vec.ColumnType)
	for _, col := range info.Columns {
		colType[col.Name] = col.Type
		if col.Type == vec.TypeVector {
			vecCol = col.Name
		}
	}

	points := make([]vec.Point, 0, len(s.Rows))
	for _, row := range s.Rows {
		p, perr := buildPoint(s.Columns, row, vecCol, colType)
		if perr != nil {
			return perr
		}
		points = append(points, p)
	}

	coll, err := c.opts.DB.Collection(s.Table)
	if err != nil {
		return c.mapError(err)
	}
	if perr := c.upsertPoints(ctx, coll, points); perr != nil {
		return perr
	}
	_ = c.w.writeCommandComplete("INSERT 0 " + itoa(len(points)))
	return nil
}

// upsertPoints writes points using the open interactive transaction when one is
// active, otherwise through UpsertBatch (spec 16 §5.2). Routing writes through
// the open txn avoids deadlocking on the single-writer lock, which UpsertBatch
// would try to take a second time.
func (c *Conn) upsertPoints(ctx context.Context, coll *vec.Collection, points []vec.Point) *pgError {
	if c.txn != nil {
		for _, p := range points {
			if _, err := coll.Upsert(c.txn, p); err != nil {
				return c.mapError(err)
			}
		}
		return nil
	}
	if _, err := coll.UpsertBatch(ctx, points); err != nil {
		return c.mapError(err)
	}
	return nil
}

// deleteIDs removes points using the open transaction when active (spec 16 §5.2).
func (c *Conn) deleteIDs(ctx context.Context, coll *vec.Collection, ids []vec.PointID) *pgError {
	if c.txn != nil {
		for _, id := range ids {
			if err := coll.Delete(c.txn, id); err != nil {
				return c.mapError(err)
			}
		}
		return nil
	}
	if err := coll.DeleteBatch(ctx, ids); err != nil {
		return c.mapError(err)
	}
	return nil
}

// buildPoint assembles one point from an INSERT row. columns may be empty, in
// which case positional order over the schema is assumed (id, then declared).
func buildPoint(columns []string, row []vectorsql.Expr, vecCol string, colType map[string]vec.ColumnType) (vec.Point, *pgError) {
	p := vec.Point{Vectors: map[string]vec.AnyVector{}, Meta: map[string]vec.Value{}}
	names := columns
	if len(names) == 0 {
		// No column list: assume id first, then the remaining columns in order.
		// Without a stable order we cannot reconstruct, so require an explicit list.
		return p, &pgError{code: "42601", message: "INSERT requires an explicit column list"}
	}
	if len(names) != len(row) {
		return p, &pgError{code: "42601", message: "INSERT column/value count mismatch"}
	}
	for i, name := range names {
		expr := row[i]
		if isIDName(name) {
			if iv, ok := litInt(expr); ok {
				p.ID = vec.IntID(uint64(iv))
			}
			continue
		}
		if name == vecCol {
			fl, perr := exprVector(expr)
			if perr != nil {
				return p, perr
			}
			p.Vectors[vecCol] = vec.AnyVector{Dense: vec.Vector(fl)}
			continue
		}
		val, perr := exprValue(expr, colType[name])
		if perr != nil {
			return p, perr
		}
		p.Meta[name] = val
	}
	return p, nil
}

// execUpdate lowers UPDATE ... SET embedding = $1 WHERE id = $2 onto an upsert
// of the affected point (spec 16 §4.5). Only id-keyed single-row updates are
// supported.
func (c *Conn) execUpdate(ctx context.Context, s *vectorsql.UpdateStmt) *pgError {
	id, ok := whereID(s.Where)
	if !ok {
		return unsupported("UPDATE supports only WHERE id = <n>")
	}
	info, err := c.opts.DB.GetCollection(ctx, s.Table)
	if err != nil {
		return c.mapError(err)
	}
	vecCol := ""
	colType := make(map[string]vec.ColumnType)
	for _, col := range info.Columns {
		colType[col.Name] = col.Type
		if col.Type == vec.TypeVector {
			vecCol = col.Name
		}
	}

	coll, err := c.opts.DB.Collection(s.Table)
	if err != nil {
		return c.mapError(err)
	}
	cur, err := coll.Get(c.txn, vec.IntID(id))
	if err != nil {
		return c.mapError(err)
	}
	p := vec.Point{ID: vec.IntID(id), Vectors: cur.Vectors, Meta: cur.Meta}
	if p.Vectors == nil {
		p.Vectors = map[string]vec.AnyVector{}
	}
	if p.Meta == nil {
		p.Meta = map[string]vec.Value{}
	}
	for _, asg := range s.Assigns {
		if asg.Column == vecCol {
			fl, perr := exprVector(asg.Value)
			if perr != nil {
				return perr
			}
			p.Vectors[vecCol] = vec.AnyVector{Dense: vec.Vector(fl)}
			continue
		}
		val, perr := exprValue(asg.Value, colType[asg.Column])
		if perr != nil {
			return perr
		}
		p.Meta[asg.Column] = val
	}
	if perr := c.upsertPoints(ctx, coll, []vec.Point{p}); perr != nil {
		return perr
	}
	_ = c.w.writeCommandComplete("UPDATE 1")
	return nil
}

// execDelete lowers DELETE FROM ... WHERE id = $1 onto DeleteBatch (spec 16 §4.5).
func (c *Conn) execDelete(ctx context.Context, s *vectorsql.DeleteStmt) *pgError {
	id, ok := whereID(s.Where)
	if !ok {
		return unsupported("DELETE supports only WHERE id = <n>")
	}
	coll, err := c.opts.DB.Collection(s.Table)
	if err != nil {
		return c.mapError(err)
	}
	if perr := c.deleteIDs(ctx, coll, []vec.PointID{vec.IntID(id)}); perr != nil {
		return perr
	}
	_ = c.w.writeCommandComplete("DELETE 1")
	return nil
}

// execDrop lowers DROP TABLE / DROP INDEX (spec 16 §4.5).
func (c *Conn) execDrop(ctx context.Context, s *vectorsql.DropStmt) *pgError {
	if s.Index {
		table := s.OnTable
		if err := c.opts.DB.DropIndex(ctx, table, s.Name); err != nil {
			if s.IfExists && isNotFound(err) {
				_ = c.w.writeCommandComplete("DROP INDEX")
				return nil
			}
			return c.mapError(err)
		}
		_ = c.w.writeCommandComplete("DROP INDEX")
		return nil
	}
	if err := c.opts.DB.DropCollection(ctx, s.Name); err != nil {
		if s.IfExists && isNotFound(err) {
			_ = c.w.writeCommandComplete("DROP TABLE")
			return nil
		}
		return c.mapError(err)
	}
	_ = c.w.writeCommandComplete("DROP TABLE")
	return nil
}

// execTxnStmt handles BEGIN/COMMIT/ROLLBACK arriving through the parser rather
// than the string interceptor (spec 16 §5.2).
func (c *Conn) execTxnStmt(s *vectorsql.TxnStmt) *pgError {
	switch s.Kind {
	case "begin":
		_, _, perr := c.handleTxnControl("begin")
		if perr != nil {
			return perr
		}
		_ = c.w.writeCommandComplete("BEGIN")
	case "commit":
		_, _, perr := c.handleTxnControl("commit")
		if perr != nil {
			return perr
		}
		_ = c.w.writeCommandComplete("COMMIT")
	case "rollback":
		_, _, perr := c.handleTxnControl("rollback")
		if perr != nil {
			return perr
		}
		_ = c.w.writeCommandComplete("ROLLBACK")
	default:
		return unsupported("transaction statement %q", s.Kind)
	}
	return nil
}

// --- literal and expression helpers ---

func isIDName(name string) bool { return name == "id" || name == "point_id" }

// whereID extracts the integer N from a WHERE id = N predicate.
func whereID(e vectorsql.Expr) (uint64, bool) {
	be, ok := e.(*vectorsql.BinaryExpr)
	if !ok || be.Op != "=" {
		return 0, false
	}
	if cr, ok := be.Left.(*vectorsql.ColumnRef); ok && isIDName(cr.Name) {
		if iv, ok := litInt(be.Right); ok {
			return uint64(iv), true
		}
	}
	if cr, ok := be.Right.(*vectorsql.ColumnRef); ok && isIDName(cr.Name) {
		if iv, ok := litInt(be.Left); ok {
			return uint64(iv), true
		}
	}
	return 0, false
}

// exprVector resolves an expression to a dense vector. A string literal in the
// pgvector text form is parsed; a cast unwraps to its operand (spec 16 §18.3).
func exprVector(e vectorsql.Expr) ([]float32, *pgError) {
	switch x := e.(type) {
	case *vectorsql.CastExpr:
		return exprVector(x.Expr)
	case *vectorsql.StringLit:
		fl, err := vectorsql.ParseVectorLiteral(x.Value)
		if err != nil {
			return nil, &pgError{code: "22P02", message: cleanErr(err)}
		}
		return fl, nil
	default:
		return nil, &pgError{code: "22P02", message: "expected a vector literal"}
	}
}

// exprValue resolves a scalar expression to a vec.Value of the target type.
func exprValue(e vectorsql.Expr, t vec.ColumnType) (vec.Value, *pgError) {
	if _, ok := e.(*vectorsql.NullLit); ok {
		return vec.NullValue(), nil
	}
	if x, ok := e.(*vectorsql.CastExpr); ok {
		return exprValue(x.Expr, t)
	}
	switch t {
	case vec.TypeInt64, vec.TypeTimestamp:
		if iv, ok := litInt(e); ok {
			return vec.IntValue(iv), nil
		}
	case vec.TypeFloat64:
		if fv, ok := litFloat(e); ok {
			return vec.FloatValue(fv), nil
		}
	case vec.TypeBool:
		if bl, ok := e.(*vectorsql.BoolLit); ok {
			return vec.BoolValue(bl.Value), nil
		}
	case vec.TypeText, vec.TypeJSON:
		if sv, ok := litString(e); ok {
			return vec.TextValue(sv), nil
		}
	case vec.TypeBytes:
		if sv, ok := litString(e); ok {
			return vec.BytesValue([]byte(sv)), nil
		}
	}
	// Fall back to a best-effort literal so loosely typed inserts still land.
	if iv, ok := litInt(e); ok {
		return vec.IntValue(iv), nil
	}
	if fv, ok := litFloat(e); ok {
		return vec.FloatValue(fv), nil
	}
	if sv, ok := litString(e); ok {
		return vec.TextValue(sv), nil
	}
	return vec.NullValue(), &pgError{code: "22P02", message: "unsupported value literal"}
}

// litInt extracts an int64 from an integer literal or unary-minus integer.
func litInt(e vectorsql.Expr) (int64, bool) {
	switch x := e.(type) {
	case *vectorsql.IntLit:
		return x.Value, true
	case *vectorsql.UnaryExpr:
		if x.Op == "-" {
			if v, ok := litInt(x.Expr); ok {
				return -v, true
			}
		}
	}
	return 0, false
}

// litFloat extracts a float64 from a float or integer literal.
func litFloat(e vectorsql.Expr) (float64, bool) {
	switch x := e.(type) {
	case *vectorsql.FloatLit:
		return x.Value, true
	case *vectorsql.IntLit:
		return float64(x.Value), true
	case *vectorsql.UnaryExpr:
		if x.Op == "-" {
			if v, ok := litFloat(x.Expr); ok {
				return -v, true
			}
		}
	}
	return 0, false
}

// litString extracts a string from a string literal.
func litString(e vectorsql.Expr) (string, bool) {
	if x, ok := e.(*vectorsql.StringLit); ok {
		return x.Value, true
	}
	return "", false
}

// isAlreadyExists and isNotFound test mapped facade errors for IfNotExists/IfExists.
func isAlreadyExists(err error) bool { return errIs(err, vec.ErrAlreadyExists) }
func isNotFound(err error) bool      { return errIs(err, vec.ErrNotFound) }
