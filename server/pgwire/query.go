package pgwire

import (
	"context"
	"errors"
	"strconv"
	"strings"

	vec "github.com/tamnd/vec"
	"github.com/tamnd/vec/vectorsql"
)

// execVectorSQL parses sql with the VectorSQL parser and dispatches DDL/DML/SELECT
// onto the vec facade (spec 16 §18.1 steps 4-7). SELECT streams a RowDescription
// and DataRow rows; DDL/DML reply with a CommandComplete tag.
func (c *Conn) execVectorSQL(ctx context.Context, sql string, resultFormats []int16) *pgError {
	stmt, err := vectorsql.Parse(sql)
	if err != nil {
		return &pgError{code: "42601", message: cleanErr(err)}
	}
	switch s := stmt.(type) {
	case *vectorsql.SelectStmt:
		return c.execSelect(ctx, sql, s, resultFormats)
	case *vectorsql.CreateTableStmt:
		return c.execCreateTable(ctx, s)
	case *vectorsql.CreateIndexStmt:
		return c.execCreateIndex(ctx, s)
	case *vectorsql.InsertStmt:
		return c.execInsert(ctx, s)
	case *vectorsql.UpdateStmt:
		return c.execUpdate(ctx, s)
	case *vectorsql.DeleteStmt:
		return c.execDelete(ctx, s)
	case *vectorsql.DropStmt:
		return c.execDrop(ctx, s)
	case *vectorsql.TxnStmt:
		return c.execTxnStmt(s)
	case *vectorsql.SetStmt:
		_ = c.w.writeCommandComplete("SET")
		return nil
	case *vectorsql.PragmaStmt:
		_ = c.w.writeCommandComplete("PRAGMA")
		return nil
	default:
		return unsupported("statement type %T", stmt)
	}
}

// execSelect runs a kNN SELECT through db.Exec and encodes the result rows
// (spec 16 §4.5, §17.4, §17.5). The original SQL goes straight to db.Exec so the
// pgvector distance operators (<->, <=>, <#>) are handled by the binder.
func (c *Conn) execSelect(ctx context.Context, sql string, sel *vectorsql.SelectStmt, resultFormats []int16) *pgError {
	cols, perr := c.selectColumns(sel)
	if perr != nil {
		return perr
	}
	rows, err := c.dbExec(ctx, sql)
	if err != nil {
		return c.mapError(err)
	}
	defer func() { _ = rows.Close() }()

	applyResultFormats(cols, resultFormats)
	if err := c.w.writeRowDescription(cols); err != nil {
		return &pgError{code: "XX000", message: err.Error()}
	}

	n := 0
	for rows.Next() {
		r := rows.Result()
		values := make([][]byte, len(cols))
		for i, oc := range outputCols(sel, cols) {
			values[i] = encodeResultValue(r, oc, cols[i].formatCode)
		}
		if err := c.w.writeDataRow(values); err != nil {
			return &pgError{code: "XX000", message: err.Error()}
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return c.mapError(err)
	}
	_ = c.w.writeCommandComplete("SELECT " + itoa(n))
	return nil
}

// dbExec runs sql, using the open interactive transaction when one is active.
func (c *Conn) dbExec(ctx context.Context, sql string) (*vec.Rows, error) {
	if c.txn != nil {
		return c.opts.DB.ExecTxn(ctx, c.txn, sql)
	}
	return c.opts.DB.Exec(ctx, sql)
}

// outCol describes one SELECT output column for value encoding.
type outCol struct {
	kind colKind
	name string // metadata column name when kind==colMeta
}

type colKind uint8

const (
	colID colKind = iota
	colDistance
	colMeta
	colExpr
)

// selectColumns derives the RowDescription fields for a SELECT projection
// (spec 16 §17.4). It resolves each select item against the collection schema:
// the id column maps to int8 OID 20, a distance expression to float8 OID 701,
// and metadata columns to their declared types.
func (c *Conn) selectColumns(sel *vectorsql.SelectStmt) ([]fieldDesc, *pgError) {
	info, err := c.opts.DB.GetCollection(context.Background(), sel.From)
	if err != nil {
		return nil, c.mapError(err)
	}
	typeByName := make(map[string]vec.ColumnType)
	for _, col := range info.Columns {
		typeByName[col.Name] = col.Type
	}

	var fields []fieldDesc
	add := func(name string, oid int32, size int16) {
		fields = append(fields, fieldDesc{name: name, colNum: int16(len(fields) + 1), typeOID: oid, typeSize: size, typeMod: -1})
	}

	if len(sel.Columns) == 0 || sel.Star {
		add("id", oidInt8, 8)
		for _, col := range info.Columns {
			if col.Type == vec.TypeVector {
				continue
			}
			oid, size := pgTypeFor(col.Type)
			add(col.Name, oid, size)
		}
		add("distance", oidFloat8, 8)
		return fields, nil
	}

	for _, item := range sel.Columns {
		name := selectItemName(item)
		switch resolveColKind(item.Expr, typeByName) {
		case colID:
			add(name, oidInt8, 8)
		case colDistance, colExpr:
			add(name, oidFloat8, 8)
		default: // colMeta
			ct := typeByName[colRefName(item.Expr)]
			oid, size := pgTypeFor(ct)
			add(name, oid, size)
		}
	}
	return fields, nil
}

// outputCols mirrors selectColumns to produce the per-column extraction plan.
func outputCols(sel *vectorsql.SelectStmt, fields []fieldDesc) []outCol {
	out := make([]outCol, 0, len(fields))
	if len(sel.Columns) == 0 || sel.Star {
		for _, f := range fields {
			switch f.name {
			case "id":
				out = append(out, outCol{kind: colID})
			case "distance":
				out = append(out, outCol{kind: colDistance})
			default:
				out = append(out, outCol{kind: colMeta, name: f.name})
			}
		}
		return out
	}
	for _, item := range sel.Columns {
		switch {
		case isIDRef(item.Expr):
			out = append(out, outCol{kind: colID})
		case exprHasDistance(item.Expr):
			out = append(out, outCol{kind: colDistance})
		default:
			name := colRefName(item.Expr)
			if name == "" {
				out = append(out, outCol{kind: colExpr})
			} else {
				out = append(out, outCol{kind: colMeta, name: name})
			}
		}
	}
	return out
}

// encodeResultValue renders one output column of a result row as wire bytes
// (spec 16 §17.5). Only text format is produced; a binary request falls back to
// the text encoding, which clients accept for these scalar types.
func encodeResultValue(r vec.Result, oc outCol, _ int16) []byte {
	switch oc.kind {
	case colID:
		return []byte(strconv.FormatUint(r.ID.N, 10))
	case colDistance, colExpr:
		return []byte(strconv.FormatFloat(float64(r.Distance), 'g', -1, 32))
	case colMeta:
		v, ok := r.Column(oc.name)
		if !ok || v.IsNull() {
			return nil
		}
		return []byte(encodeValueText(v))
	default:
		return nil
	}
}

// encodeValueText renders a metadata value as PG text (spec 16 §17.5).
func encodeValueText(v vec.Value) string {
	switch v.Type() {
	case vec.TypeInt64:
		return strconv.FormatInt(v.Int(), 10)
	case vec.TypeFloat64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case vec.TypeBool:
		if v.Bool() {
			return "t"
		}
		return "f"
	case vec.TypeText, vec.TypeJSON:
		return v.Text()
	case vec.TypeBytes:
		return string(v.Bytes())
	default:
		return v.String()
	}
}

// pgTypeFor maps a vec column type to a PG type OID and size (spec 16 §4.4).
func pgTypeFor(t vec.ColumnType) (int32, int16) {
	switch t {
	case vec.TypeInt64:
		return oidInt8, 8
	case vec.TypeFloat64:
		return oidFloat8, 8
	case vec.TypeBool:
		return oidBool, 1
	case vec.TypeText, vec.TypeJSON:
		return oidText, -1
	case vec.TypeBytes:
		return oidBytea, -1
	case vec.TypeTimestamp:
		return oidInt8, 8
	default:
		return oidText, -1
	}
}

// --- select-item analysis helpers ---

func resolveColKind(e vectorsql.Expr, types map[string]vec.ColumnType) colKind {
	if isIDRef(e) {
		return colID
	}
	if exprHasDistance(e) {
		return colDistance
	}
	if name := colRefName(e); name != "" {
		if _, ok := types[name]; ok {
			return colMeta
		}
	}
	return colExpr
}

func isIDRef(e vectorsql.Expr) bool {
	cr, ok := e.(*vectorsql.ColumnRef)
	return ok && (cr.Name == "id" || cr.Name == "point_id")
}

// exprHasDistance reports whether e contains a vector distance operator.
func exprHasDistance(e vectorsql.Expr) bool {
	switch x := e.(type) {
	case *vectorsql.DistanceExpr:
		return true
	case *vectorsql.BinaryExpr:
		return exprHasDistance(x.Left) || exprHasDistance(x.Right)
	case *vectorsql.UnaryExpr:
		return exprHasDistance(x.Expr)
	case *vectorsql.CastExpr:
		return exprHasDistance(x.Expr)
	case *vectorsql.FuncCall:
		for _, a := range x.Args {
			if exprHasDistance(a) {
				return true
			}
		}
	}
	return false
}

// colRefName returns the bare column name if e is a column reference.
func colRefName(e vectorsql.Expr) string {
	if cr, ok := e.(*vectorsql.ColumnRef); ok {
		return cr.Name
	}
	return ""
}

// selectItemName returns the output column name for a select item (alias, column
// name, or a synthetic name).
func selectItemName(item vectorsql.ExprAlias) string {
	if item.Alias != "" {
		return item.Alias
	}
	if name := colRefName(item.Expr); name != "" {
		return name
	}
	if exprHasDistance(item.Expr) {
		return "distance"
	}
	return "?column?"
}

// cleanErr renders a parser or facade error without the internal vec: prefix.
func cleanErr(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, "vec: ")
	return s
}

// unsupported builds a 0A000 feature-not-supported error (spec 16 §4.7).
func unsupported(format string, args ...any) *pgError {
	return &pgError{
		code:    "0A000",
		message: "feature not supported: " + sprintf(format, args...),
		hint:    "vec supports the pgvector kNN subset; see documentation",
	}
}

// mapError maps a vec facade error to a PG SQLSTATE (spec 16 §4.7). Not found is
// 42P01, dimension/schema is 22000/42804, unsupported is 0A000, auth is 28P01,
// and everything else is XX000.
func (c *Conn) mapError(err error) *pgError {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, vec.ErrNotFound):
		return &pgError{code: "42P01", message: cleanErr(err)}
	case errors.Is(err, vec.ErrAlreadyExists):
		return &pgError{code: "42P07", message: cleanErr(err)}
	case errors.Is(err, vec.ErrDimMismatch):
		return &pgError{code: "22000", message: cleanErr(err)}
	case errors.Is(err, vec.ErrSchemaViolation), errors.Is(err, vec.ErrUnknownColumn):
		return &pgError{code: "42804", message: cleanErr(err)}
	case errors.Is(err, vec.ErrConflict):
		return &pgError{code: "40001", message: cleanErr(err)}
	case errors.Is(err, vec.ErrReadOnly):
		return &pgError{code: "25006", message: cleanErr(err)}
	case errors.Is(err, vec.ErrCanceled):
		return &pgError{code: "57014", message: cleanErr(err)}
	default:
		// errUnsupported is unexported; match it by its message.
		if strings.Contains(err.Error(), "not yet supported") || strings.Contains(err.Error(), "needs a literal vector") {
			return unsupported("%s", cleanErr(err))
		}
		return &pgError{code: "XX000", message: cleanErr(err)}
	}
}

// --- small integer formatters used across the package ---

func itoa(n int) string     { return strconv.Itoa(n) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
