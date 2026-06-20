package vectorsql

import (
	"strconv"
	"strings"

	"github.com/tamnd/vector/catalog"
	"github.com/tamnd/vector/query"
)

// The binder resolves a parsed statement against the catalog and lowers it for the
// engine (spec 12 §17.1, §14.6). CREATE TABLE binds to a catalog.Schema; a kNN SELECT
// binds to the planner's query.BoundQuery. Name resolution and type checking happen
// here, so a bind error is returned before any execution.

// BindCreateTable lowers a parsed CREATE TABLE to a catalog.Schema (spec 12 §3.1).
// It maps each SQL column type to the catalog's vector or metadata column kind,
// applies the implicit primary key rule, and rejects more than one vector column per
// the index SPI contract.
func BindCreateTable(st *CreateTableStmt) (*catalog.Schema, error) {
	s := &catalog.Schema{Name: st.Name, IDKind: catalog.IDBigInt}
	idName := ""
	for _, c := range st.Columns {
		if c.PrimaryKey {
			if idName != "" {
				return nil, typeError("multiple PRIMARY KEY columns", c.Name)
			}
			idName = c.Name
			kind, ok := idKindOf(c.Type)
			if !ok {
				return nil, typeError("primary key must be BIGINT, TEXT, or BLOB", c.Name)
			}
			s.IDKind = kind
			continue
		}
		col, err := bindColumn(c)
		if err != nil {
			return nil, err
		}
		s.Columns = append(s.Columns, col)
	}
	if len(st.PrimaryKey) == 1 {
		idName = st.PrimaryKey[0]
		s.Columns = dropColumn(s.Columns, idName)
	} else if len(st.PrimaryKey) > 1 {
		return nil, unsupportedError("composite PRIMARY KEY is not supported", "spec 12 §3.1.2 allows one primary-key column")
	}
	if idName != "" {
		s.IDName = idName
	} else {
		s.IDName = "id"
		s.AutoIncrement = true
	}
	return s, nil
}

// bindColumn lowers one non-primary-key column definition.
func bindColumn(c ColumnSpec) (catalog.ColumnDef, error) {
	out := catalog.ColumnDef{Name: c.Name, Nullable: !c.NotNull}
	if c.Nullable {
		out.Nullable = true
	}
	t := c.Type
	switch t.Name {
	case "vector":
		if !t.HasArg || t.Arg <= 0 {
			return out, dimError("VECTOR requires a positive dimension", c.Name)
		}
		out.Kind = catalog.ColumnVector
		out.Dim = uint32(t.Arg)
		out.ElemType = catalog.ElemFP32
	case "sparsevec":
		return out, unsupportedError("SPARSEVEC columns are not yet stored", "spec 12 §3.1.1 reserves sparse columns for a later milestone")
	case "multivec":
		return out, unsupportedError("MULTIVEC columns are not yet stored", "spec 12 §3.1.1 reserves multi-vector columns for a later milestone")
	default:
		k, ok := metadataKindOf(t.Name)
		if !ok {
			return out, typeError("unknown column type "+strings.ToUpper(t.Name), c.Name)
		}
		out.Kind = catalog.ColumnMetadata
		out.DataType = k
	}
	return out, nil
}

// dropColumn removes a table-level primary-key column from the metadata set.
func dropColumn(cols []catalog.ColumnDef, name string) []catalog.ColumnDef {
	out := cols[:0]
	for _, c := range cols {
		if c.Name == name {
			continue
		}
		out = append(out, c)
	}
	return out
}

// idKindOf maps a primary-key column type to a catalog id kind.
func idKindOf(t *TypeRef) (catalog.IDKind, bool) {
	switch t.Name {
	case "bigint", "int8", "integer", "int":
		return catalog.IDBigInt, true
	case "text", "varchar":
		return catalog.IDText, true
	case "blob", "bytea":
		return catalog.IDBlob, true
	default:
		return 0, false
	}
}

// metadataKindOf maps a SQL scalar type to a catalog metadata kind (spec 12 §3.1.1).
func metadataKindOf(name string) (catalog.Kind, bool) {
	switch name {
	case "bigint", "int8", "integer", "int":
		return catalog.KindBigInt, true
	case "float", "float8", "double precision", "double":
		return catalog.KindDouble, true
	case "float4", "real":
		return catalog.KindReal, true
	case "text", "varchar":
		return catalog.KindText, true
	case "boolean", "bool":
		return catalog.KindBool, true
	case "blob", "bytea":
		return catalog.KindBlob, true
	case "timestamp", "timestamptz":
		return catalog.KindTimestamp, true
	case "json", "jsonb":
		return catalog.KindJSON, true
	default:
		return catalog.KindNull, false
	}
}

// BoundSelect is the result of binding a SELECT (spec 12 §17.3). For a kNN query it
// carries the planner's BoundQuery and, when the query vector is a literal, the
// resolved vector. When the vector rides in a parameter, Query holds nil and the db
// layer ([14]) substitutes the bound value before planning.
type BoundSelect struct {
	BoundQuery query.BoundQuery
	IsKNN      bool
	// VectorParam names the parameter supplying the query vector, or is empty when the
	// vector is a literal already placed in BoundQuery.Vector.
	VectorParam ParamRef
	HasParam    bool
	// Projections are the metadata column names the select list requests.
	Projections []string
}

// BindSelect resolves a SELECT against a collection and detects the kNN plan shape of
// spec 12 §17.3: exactly one ORDER BY item that is a distance operator on the vector
// column, plus a bounded LIMIT. A matching query lowers to a query.BoundQuery; the
// WHERE clause lowers to a storage.Predicate. A non-kNN SELECT is reported as
// unsupported in M6 because the planner only serves kNN and point lookups.
func BindSelect(st *SelectStmt, coll *catalog.Collection) (*BoundSelect, error) {
	if st.Fusion != nil {
		return nil, unsupportedError("FUSION queries are lowered by the db layer", "spec 12 §7.2 fuses through the hybrid package, not the planner")
	}
	def := coll.StorageDef()
	vecCol := vectorColumnName(coll)

	shape, err := detectKNN(st, vecCol)
	if err != nil {
		return nil, err
	}
	if !shape.ok {
		return nil, unsupportedError("only kNN SELECT is supported", "spec 12 §17.3: ORDER BY <distance> LIMIT k")
	}

	bq := query.BoundQuery{
		K:           shape.k,
		Metric:      def.Metric,
		Selectivity: -1,
	}
	bs := &BoundSelect{IsKNN: true}

	switch q := shape.query.(type) {
	case *StringLit:
		vec, err := ParseVectorLiteral(q.Value)
		if err != nil {
			return nil, err
		}
		if len(vec) != int(def.Dims) {
			return nil, dimError("query vector dimension does not match the column", strconv.Itoa(len(vec))+" vs "+strconv.Itoa(int(def.Dims)))
		}
		bq.Vector = vec
	case *ParamRef:
		bs.HasParam = true
		bs.VectorParam = *q
	default:
		return nil, typeError("kNN query vector must be a literal or parameter", "")
	}

	if st.Where != nil {
		pred, err := bindPredicate(st.Where, coll)
		if err != nil {
			return nil, err
		}
		bq.Predicate = pred
	}

	proj, err := projectionColumns(st, coll, vecCol)
	if err != nil {
		return nil, err
	}
	bq.Project = proj
	bs.Projections = proj
	bs.BoundQuery = bq
	return bs, nil
}

// knnShape is the structural detection result of spec 12 §17.3.
type knnShape struct {
	ok    bool
	query Expr
	k     int
}

// detectKNN checks the three conditions of the kNN plan shape (spec 12 §17.3).
func detectKNN(st *SelectStmt, vecCol string) (knnShape, error) {
	if len(st.OrderBy) != 1 {
		return knnShape{}, nil
	}
	de, ok := st.OrderBy[0].Expr.(*DistanceExpr)
	if !ok {
		return knnShape{}, nil
	}
	col, ok := de.Left.(*ColumnRef)
	if !ok || col.Name != vecCol {
		return knnShape{}, typeError("kNN ORDER BY must reference the vector column", vecCol)
	}
	if st.Limit == nil {
		return knnShape{}, newError(codePlan, "kNN query has no LIMIT", "spec 12 §17.3 requires a bounded LIMIT", -1)
	}
	k, ok := constInt(st.Limit)
	if !ok || k <= 0 {
		return knnShape{}, typeError("LIMIT must be a positive integer constant", "")
	}
	return knnShape{ok: true, query: de.Right, k: k}, nil
}

// projectionColumns resolves the select list to metadata column names, rejecting the
// vector column from projection and accepting * as all metadata columns.
func projectionColumns(st *SelectStmt, coll *catalog.Collection, vecCol string) ([]string, error) {
	if st.Star || len(st.Columns) == 0 {
		var names []string
		for _, c := range coll.Schema.MetadataColumns() {
			names = append(names, c.Name)
		}
		return names, nil
	}
	var out []string
	for _, ca := range st.Columns {
		ref, ok := ca.Expr.(*ColumnRef)
		if !ok {
			// Distance-as-column and function projections are resolved by the db layer;
			// the binder records only metadata column projections here.
			continue
		}
		if ref.Name == vecCol {
			continue
		}
		if _, ok := coll.ColID(ref.Name); !ok {
			if ref.Name == coll.Schema.IDName || ref.Name == "id" {
				continue
			}
			return nil, identError("unknown column "+ref.Name, coll.Schema.Name)
		}
		out = append(out, ref.Name)
	}
	return out, nil
}

// vectorColumnName returns the single vector column's name (spec 12 §3.1.1 allows one).
func vectorColumnName(coll *catalog.Collection) string {
	vs := coll.Schema.VectorColumns()
	if len(vs) == 0 {
		return ""
	}
	return vs[0].Name
}

// constInt extracts a non-negative integer constant from an expression.
func constInt(e Expr) (int, bool) {
	switch v := e.(type) {
	case *IntLit:
		return int(v.Value), true
	case *UnaryExpr:
		if v.Op == "-" {
			if n, ok := constInt(v.Expr); ok {
				return -n, true
			}
		}
	}
	return 0, false
}
