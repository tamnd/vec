package vectorsql

import (
	"strconv"
	"strings"

	"github.com/tamnd/vector/catalog"
	"github.com/tamnd/vector/storage"
)

// This file lowers a parsed WHERE expression to a storage.Predicate (spec 12 §17.4)
// and parses the vector string literal form (spec 12 §2.6). Only the metadata
// predicate fragment the storage engine can evaluate is accepted here; anything the
// engine cannot push down is reported as unsupported so the planner never sees it.

// bindPredicate translates a boolean WHERE expression into a storage.Predicate. The
// supported shape is the conjunction/disjunction of column-versus-literal comparisons,
// IS [NOT] NULL, and NOT, matching storage.Predicate's evaluable forms.
func bindPredicate(e Expr, coll *catalog.Collection) (storage.Predicate, error) {
	switch n := e.(type) {
	case *BinaryExpr:
		switch n.Op {
		case "and":
			return bindBoolChain(n, coll, true)
		case "or":
			return bindBoolChain(n, coll, false)
		case "=", "<>", "<", "<=", ">", ">=":
			return bindCompare(n, coll)
		default:
			return nil, unsupportedError("operator "+n.Op+" is not pushable into a filter", "spec 12 §17.4 lists the comparison operators a WHERE clause may use")
		}
	case *UnaryExpr:
		if n.Op == "not" {
			inner, err := bindPredicate(n.Expr, coll)
			if err != nil {
				return nil, err
			}
			return storage.Not{Term: inner}, nil
		}
		return nil, unsupportedError("unary "+n.Op+" is not a boolean predicate", "")
	case *IsNullExpr:
		col, err := resolveColumn(n.Expr, coll)
		if err != nil {
			return nil, err
		}
		return storage.IsNullPred{Col: col, Negate: n.Not}, nil
	default:
		return nil, unsupportedError("this WHERE form is not pushable into a filter", "spec 12 §17.4")
	}
}

// bindBoolChain flattens a left-deep AND/OR tree into a single And/Or node so the
// storage evaluator sees one flat term list.
func bindBoolChain(n *BinaryExpr, coll *catalog.Collection, isAnd bool) (storage.Predicate, error) {
	var terms []storage.Predicate
	var walk func(e Expr) error
	op := "and"
	if !isAnd {
		op = "or"
	}
	walk = func(e Expr) error {
		if b, ok := e.(*BinaryExpr); ok && b.Op == op {
			if err := walk(b.Left); err != nil {
				return err
			}
			return walk(b.Right)
		}
		p, err := bindPredicate(e, coll)
		if err != nil {
			return err
		}
		terms = append(terms, p)
		return nil
	}
	if err := walk(n); err != nil {
		return nil, err
	}
	if isAnd {
		return storage.And{Terms: terms}, nil
	}
	return storage.Or{Terms: terms}, nil
}

// bindCompare lowers a single column-versus-literal comparison. The column may sit on
// either side; the operator is flipped when the literal comes first.
func bindCompare(n *BinaryExpr, coll *catalog.Collection) (storage.Predicate, error) {
	op, ok := cmpOpOf(n.Op)
	if !ok {
		return nil, unsupportedError("operator "+n.Op+" is not a comparison", "")
	}
	colExpr, litExpr, flipped := n.Left, n.Right, false
	if !isColumn(colExpr) {
		colExpr, litExpr, flipped = n.Right, n.Left, true
	}
	if !isColumn(colExpr) {
		return nil, unsupportedError("a filter comparison must reference a column", "spec 12 §17.4")
	}
	col, err := resolveColumn(colExpr, coll)
	if err != nil {
		return nil, err
	}
	cd := columnDef(coll, colExpr)
	lit, err := bindLiteral(litExpr, cd)
	if err != nil {
		return nil, err
	}
	if flipped {
		op = flipOp(op)
	}
	return storage.Compare{Col: col, Op: op, Lit: lit}, nil
}

func cmpOpOf(s string) (storage.CmpOp, bool) {
	switch s {
	case "=":
		return storage.OpEq, true
	case "<>":
		return storage.OpNe, true
	case "<":
		return storage.OpLt, true
	case "<=":
		return storage.OpLe, true
	case ">":
		return storage.OpGt, true
	case ">=":
		return storage.OpGe, true
	default:
		return 0, false
	}
}

// flipOp mirrors a comparison operator so `5 < col` becomes `col > 5`.
func flipOp(op storage.CmpOp) storage.CmpOp {
	switch op {
	case storage.OpLt:
		return storage.OpGt
	case storage.OpLe:
		return storage.OpGe
	case storage.OpGt:
		return storage.OpLt
	case storage.OpGe:
		return storage.OpLe
	default:
		return op
	}
}

func isColumn(e Expr) bool {
	_, ok := e.(*ColumnRef)
	return ok
}

// resolveColumn maps a column reference to its storage column id, rejecting unknown
// names and the vector column (a vector cannot appear in a scalar filter).
func resolveColumn(e Expr, coll *catalog.Collection) (storage.ColID, error) {
	ref, ok := e.(*ColumnRef)
	if !ok {
		return 0, unsupportedError("expected a column reference", "")
	}
	id, ok := coll.ColID(ref.Name)
	if !ok {
		return 0, identError("unknown column "+ref.Name, coll.Schema.Name)
	}
	cd := coll.Schema.Column(ref.Name)
	if cd != nil && cd.IsVector() {
		return 0, typeError("vector column "+ref.Name+" cannot appear in a scalar filter", "")
	}
	return id, nil
}

func columnDef(coll *catalog.Collection, e Expr) *catalog.ColumnDef {
	ref, ok := e.(*ColumnRef)
	if !ok {
		return nil
	}
	return coll.Schema.Column(ref.Name)
}

// bindLiteral converts a literal expression to a storage.Value, coercing to the
// column's declared kind so the comparison matches stored values exactly.
func bindLiteral(e Expr, cd *catalog.ColumnDef) (storage.Value, error) {
	switch v := e.(type) {
	case *IntLit:
		return coerceInt(v.Value, cd)
	case *FloatLit:
		return storage.Float(v.Value), nil
	case *StringLit:
		return storage.Text(v.Value), nil
	case *BoolLit:
		return storage.Bool(v.Value), nil
	case *UnaryExpr:
		if v.Op == "-" {
			inner, err := bindLiteral(v.Expr, cd)
			if err != nil {
				return storage.Value{}, err
			}
			switch inner.Kind {
			case storage.KindInt:
				return storage.Int(-inner.I), nil
			case storage.KindFloat:
				return storage.Float(-inner.F), nil
			}
		}
		return storage.Value{}, typeError("non-constant expression in a filter literal", "")
	case *NullLit:
		return storage.Value{}, typeError("use IS NULL to test for NULL, not = NULL", "")
	default:
		return storage.Value{}, unsupportedError("a filter literal must be a constant", "spec 12 §17.4")
	}
}

// coerceInt maps an integer literal to the column's numeric kind, widening to float
// when the column is a floating type or a timestamp count.
func coerceInt(n int64, cd *catalog.ColumnDef) (storage.Value, error) {
	if cd == nil {
		return storage.Int(n), nil
	}
	switch cd.DataType {
	case catalog.KindDouble, catalog.KindReal:
		return storage.Float(float64(n)), nil
	case catalog.KindTimestamp:
		return storage.Timestamp(n), nil
	default:
		return storage.Int(n), nil
	}
}

// ParseVectorLiteral parses the textual vector form `[x, y, z]` into a float32 slice
// (spec 12 §2.6). Whitespace around brackets and commas is tolerated; an empty vector
// and a non-numeric element are both errors.
func ParseVectorLiteral(s string) ([]float32, error) {
	t := strings.TrimSpace(s)
	if len(t) < 2 || t[0] != '[' || t[len(t)-1] != ']' {
		return nil, typeError("vector literal must be wrapped in [ ]", s)
	}
	body := strings.TrimSpace(t[1 : len(t)-1])
	if body == "" {
		return nil, dimError("vector literal has no elements", s)
	}
	parts := strings.Split(body, ",")
	out := make([]float32, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, typeError("invalid vector element "+strings.TrimSpace(p), s)
		}
		out = append(out, float32(f))
	}
	return out, nil
}
