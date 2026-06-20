package storage

// CmpOp is a scalar comparison operator in a metadata predicate (spec 04 §10.5).
type CmpOp uint8

const (
	OpEq CmpOp = iota
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
)

// Predicate is a boolean metadata filter the engine evaluates against each live
// position (spec 04 §10.5, §21.1). The executor builds these from the WHERE clause
// ([11], [13]); the engine evaluates them and prunes whole zone blocks where it can.
type Predicate interface {
	// eval reports whether the row passes. get returns the column value (NullValue
	// for a missing column); SQL three-valued NULL semantics are handled per node.
	eval(get func(ColID) Value) bool
	// columns appends the column ids this predicate reads, for projection planning.
	columns(dst []ColID) []ColID
}

// Compare is a leaf predicate: column Col compared by Op against literal Lit
// (spec 04 §10.5). A comparison involving NULL is false (SQL UNKNOWN treated as
// not-passing for filter purposes).
type Compare struct {
	Col ColID
	Op  CmpOp
	Lit Value
}

func (c Compare) eval(get func(ColID) Value) bool {
	v := get(c.Col)
	if v.IsNull() || c.Lit.IsNull() {
		return false
	}
	switch c.Op {
	case OpEq:
		return v.equal(c.Lit)
	case OpNe:
		return !v.equal(c.Lit)
	case OpLt:
		return v.less(c.Lit)
	case OpLe:
		return v.less(c.Lit) || v.equal(c.Lit)
	case OpGt:
		return c.Lit.less(v)
	case OpGe:
		return c.Lit.less(v) || v.equal(c.Lit)
	default:
		return false
	}
}

func (c Compare) columns(dst []ColID) []ColID { return append(dst, c.Col) }

// IsNullPred passes when the column is NULL (spec 04 §5.3).
type IsNullPred struct {
	Col    ColID
	Negate bool // when true, passes when the column is NOT NULL
}

func (p IsNullPred) eval(get func(ColID) Value) bool {
	isNull := get(p.Col).IsNull()
	if p.Negate {
		return !isNull
	}
	return isNull
}

func (p IsNullPred) columns(dst []ColID) []ColID { return append(dst, p.Col) }

// And passes when every child passes (spec 04 §10.5).
type And struct{ Terms []Predicate }

func (a And) eval(get func(ColID) Value) bool {
	for _, t := range a.Terms {
		if !t.eval(get) {
			return false
		}
	}
	return true
}

func (a And) columns(dst []ColID) []ColID {
	for _, t := range a.Terms {
		dst = t.columns(dst)
	}
	return dst
}

// Or passes when any child passes.
type Or struct{ Terms []Predicate }

func (o Or) eval(get func(ColID) Value) bool {
	for _, t := range o.Terms {
		if t.eval(get) {
			return true
		}
	}
	return false
}

func (o Or) columns(dst []ColID) []ColID {
	for _, t := range o.Terms {
		dst = t.columns(dst)
	}
	return dst
}

// Not inverts its child.
type Not struct{ Term Predicate }

func (n Not) eval(get func(ColID) Value) bool { return !n.Term.eval(get) }
func (n Not) columns(dst []ColID) []ColID     { return n.Term.columns(dst) }

// True passes every row; the absence of a WHERE clause.
type True struct{}

func (True) eval(func(ColID) Value) bool { return true }
func (True) columns(dst []ColID) []ColID { return dst }

// blockSkippable reports whether a single zone block lets the engine skip an
// entire run of positions for this predicate (spec 04 §5.4). Only a top-level AND
// of single-column comparisons can be pushed soundly: if any conjunct is provably
// false for the whole block, the block is skippable. Anything else returns false
// (the engine evaluates the block row by row).
func blockSkippable(pred Predicate, zones map[ColID]*ZoneMap, blockIdx int) bool {
	switch p := pred.(type) {
	case Compare:
		zm := zones[p.Col]
		if zm == nil || blockIdx >= len(zm.Blocks) {
			return false
		}
		return zm.Blocks[blockIdx].canSkip(p.Op, p.Lit)
	case And:
		for _, t := range p.Terms {
			if blockSkippable(t, zones, blockIdx) {
				return true
			}
		}
		return false
	default:
		return false
	}
}
