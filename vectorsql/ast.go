package vectorsql

// This file defines the AST the parser emits (spec 12 §17.2). Every statement type is
// a Stmt; every value-producing fragment is an Expr. The binder ([14], task 15) walks
// these nodes to resolve names and lower a kNN SELECT into the planner's BoundQuery.

// Stmt is the marker interface for a top-level statement.
type Stmt interface{ stmtNode() }

// Expr is the marker interface for an expression node.
type Expr interface{ exprNode() }

// DistanceOp identifies a vector distance operator (spec 12 §17.2). The values match
// the four operators of the grammar.
type DistanceOp uint8

const (
	OpL2Distance     DistanceOp = iota // <->
	OpCosineDistance                   // <=>
	OpNegInnerProd                     // <#>
	OpL1Distance                       // <+>
)

// String renders the operator's source token.
func (o DistanceOp) String() string {
	switch o {
	case OpL2Distance:
		return "<->"
	case OpCosineDistance:
		return "<=>"
	case OpNegInnerProd:
		return "<#>"
	case OpL1Distance:
		return "<+>"
	default:
		return "<?>"
	}
}

// --- expressions ---

// ColumnRef is a column reference, optionally table-qualified (spec 12 §17.2).
type ColumnRef struct {
	Table  string // qualifier, "" when unqualified
	Name   string
	Offset int
}

// Star is the * select-list or count(*) wildcard.
type Star struct{ Offset int }

// IntLit is an integer literal already parsed to int64 (spec 12 §2.5.1).
type IntLit struct {
	Value  int64
	Offset int
}

// FloatLit is a float literal parsed to float64 (spec 12 §2.5.2).
type FloatLit struct {
	Value  float64
	Offset int
}

// StringLit is a decoded string literal; it may later coerce to a vector, sparse, or
// multivec value at bind time (spec 12 §2.5.3 through §2.5.6).
type StringLit struct {
	Value  string
	Offset int
}

// BoolLit is TRUE or FALSE.
type BoolLit struct {
	Value  bool
	Offset int
}

// NullLit is the SQL NULL.
type NullLit struct{ Offset int }

// ParamRef is a named (:name) or positional ($N) parameter (spec 12 §2.6).
type ParamRef struct {
	Name   string // for :name; "" for positional
	Pos    int    // for $N, 1-based; 0 for named
	Offset int
}

// UnaryExpr is a prefix operator: NOT or unary minus.
type UnaryExpr struct {
	Op   string // "not" or "-"
	Expr Expr
}

// BinaryExpr is an infix operator (logical, comparison, arithmetic, concat, or array
// containment); Op holds the canonical operator text.
type BinaryExpr struct {
	Op    string
	Left  Expr
	Right Expr
}

// DistanceExpr is the kNN-defining operator: column DistanceOp query (spec 12 §17.2).
type DistanceExpr struct {
	Op    DistanceOp
	Left  Expr
	Right Expr
}

// IsNullExpr is `expr IS [NOT] NULL`.
type IsNullExpr struct {
	Expr Expr
	Not  bool
}

// BetweenExpr is `expr [NOT] BETWEEN lo AND hi`.
type BetweenExpr struct {
	Expr Expr
	Lo   Expr
	Hi   Expr
	Not  bool
}

// InExpr is `expr [NOT] IN (list)`. Sub is a subquery alternative; List is the value
// list form. Exactly one is set.
type InExpr struct {
	Expr Expr
	List []Expr
	Sub  *SelectStmt
	Not  bool
}

// LikeExpr is `expr [NOT] LIKE/ILIKE pattern`.
type LikeExpr struct {
	Expr    Expr
	Pattern Expr
	Not     bool
	Insens  bool // ILIKE
}

// CastExpr is `expr :: type` (spec 12 §6.4).
type CastExpr struct {
	Expr Expr
	Type *TypeRef
}

// JSONExpr is `expr -> 'k'` or `expr ->> 'k'` (spec 12 §6.5). Text is true for ->>.
type JSONExpr struct {
	Expr Expr
	Key  string
	Text bool
}

// FuncCall is a function application, optionally with DISTINCT args or a FILTER clause
// (spec 12 §8.1).
type FuncCall struct {
	Name     string
	Args     []Expr
	Star     bool // count(*)
	Distinct bool
	Filter   Expr // FILTER (WHERE expr), nil if absent
	Offset   int
}

// CaseExpr is a searched or simple CASE expression (spec 12 §19.13).
type CaseExpr struct {
	Operand Expr // nil for a searched CASE
	Whens   []WhenClause
	Else    Expr
}

// WhenClause is one WHEN/THEN arm of a CASE.
type WhenClause struct {
	When Expr
	Then Expr
}

func (*ColumnRef) exprNode()    {}
func (*Star) exprNode()         {}
func (*IntLit) exprNode()       {}
func (*FloatLit) exprNode()     {}
func (*StringLit) exprNode()    {}
func (*BoolLit) exprNode()      {}
func (*NullLit) exprNode()      {}
func (*ParamRef) exprNode()     {}
func (*UnaryExpr) exprNode()    {}
func (*BinaryExpr) exprNode()   {}
func (*DistanceExpr) exprNode() {}
func (*IsNullExpr) exprNode()   {}
func (*BetweenExpr) exprNode()  {}
func (*InExpr) exprNode()       {}
func (*LikeExpr) exprNode()     {}
func (*CastExpr) exprNode()     {}
func (*JSONExpr) exprNode()     {}
func (*FuncCall) exprNode()     {}
func (*CaseExpr) exprNode()     {}

// --- shared clause structures ---

// TypeRef is a column type with its optional dimension or length argument (spec 12
// §3.1.1). Name is the canonical lowercased type keyword; Arg is the parenthesized
// integer for VECTOR/SPARSEVEC/MULTIVEC/VARCHAR.
type TypeRef struct {
	Name   string
	Arg    int
	HasArg bool
}

// ExprAlias is one select-list or RETURNING item: an expression with an optional AS
// alias (spec 12 §5.2).
type ExprAlias struct {
	Expr  Expr
	Alias string
}

// OrderItem is one ORDER BY term (spec 12 §5.4).
type OrderItem struct {
	Expr       Expr
	Desc       bool
	NullsFirst bool
	NullsSet   bool // whether NULLS FIRST/LAST was given explicitly
}

// Assignment is one `col = expr` of an UPDATE SET or ON CONFLICT DO UPDATE.
type Assignment struct {
	Column string
	Value  Expr
}

// --- statements ---

// SelectStmt is a SELECT (spec 12 §5.1, §17.2).
type SelectStmt struct {
	Distinct   bool
	DistinctOn []Expr
	Columns    []ExprAlias // empty means SELECT *
	Star       bool
	From       string
	FromAlias  string
	Where      Expr
	GroupBy    []Expr
	Having     Expr
	Fusion     *FusionClause
	OrderBy    []OrderItem
	Limit      Expr
	Offset     Expr
}

// FusionClause is the hybrid-search FUSION clause (spec 12 §7.2).
type FusionClause struct {
	Streams []FusionStream
	Options map[string]Expr
}

// FusionStream is one input to a FUSION: a dense vector kNN stream or a keyword match
// stream (spec 12 §7.2, §7.4).
type FusionStream struct {
	Keyword bool // false = VECTOR stream, true = KEYWORD stream

	// VECTOR stream: column DistanceOp query.
	Column string
	Op     DistanceOp
	Query  Expr
	// KEYWORD stream: MATCH (cols) AGAINST (query).
	MatchCols []string
	Against   Expr
	Options   map[string]Expr
}

// ColumnSpec is one column definition of a CREATE TABLE (spec 12 §3.1).
type ColumnSpec struct {
	Name       string
	Type       *TypeRef
	NotNull    bool
	Nullable   bool
	PrimaryKey bool
	Unique     bool
	Default    Expr
}

// CreateTableStmt is CREATE TABLE (spec 12 §3.1).
type CreateTableStmt struct {
	IfNotExists bool
	Name        string
	Columns     []ColumnSpec
	PrimaryKey  []string // table-level PRIMARY KEY columns
	Uniques     [][]string
	Checks      []Expr
}

// CreateIndexStmt is CREATE INDEX (spec 12 §3.2, §17.2).
type CreateIndexStmt struct {
	Unique      bool
	IfNotExists bool
	Name        string
	Table       string
	IndexType   string // hnsw | ivfflat | ivfpq | diskann | flat | fts5
	Column      string
	Opclass     string
	Options     map[string]Expr
	Where       Expr
}

// DropStmt is DROP TABLE or DROP INDEX (spec 12 §3.3).
type DropStmt struct {
	Index    bool // false = TABLE, true = INDEX
	IfExists bool
	Name     string
	OnTable  string // for DROP INDEX ON table(col)
	OnColumn string
}

// AlterTableStmt is ALTER TABLE (spec 12 §3.4).
type AlterTableStmt struct {
	Table     string
	Action    string // "add_column" | "drop_column" | "rename_table" | "rename_column"
	Column    *ColumnSpec
	DropCol   string
	NewName   string
	OldColumn string
}

// InsertStmt is INSERT or UPSERT (spec 12 §4.1, §4.2).
type InsertStmt struct {
	Table      string
	Columns    []string
	Rows       [][]Expr
	OnConflict *OnConflict
	Returning  []ExprAlias
	ReturnAll  bool
}

// OnConflict is the ON CONFLICT clause of an upsert (spec 12 §4.2).
type OnConflict struct {
	Columns   []string
	DoNothing bool
	Assigns   []Assignment
}

// DeleteStmt is DELETE (spec 12 §4.3).
type DeleteStmt struct {
	Table     string
	Where     Expr
	Returning []ExprAlias
	ReturnAll bool
}

// UpdateStmt is UPDATE (spec 12 §4.4).
type UpdateStmt struct {
	Table     string
	Assigns   []Assignment
	Where     Expr
	Returning []ExprAlias
	ReturnAll bool
}

// CopyStmt is COPY ... FROM (spec 12 §4.5).
type CopyStmt struct {
	Table   string
	Columns []string
	Source  string // file path, or "" for STDIN
	Stdin   bool
	Format  string // "jsonl" | "csv" | "binary"
	Options map[string]Expr
}

// TxnStmt is a transaction-control statement (spec 12 §11.1).
type TxnStmt struct {
	Kind      string // "begin" | "commit" | "rollback" | "savepoint" | "release" | "rollback_to"
	Isolation string // for begin
	Name      string // savepoint/release/rollback-to name
}

// PrepareStmt is PREPARE (spec 12 §9.2).
type PrepareStmt struct {
	Name  string
	Types []*TypeRef
	Body  Stmt
}

// ExecuteStmt is EXECUTE (spec 12 §9.2).
type ExecuteStmt struct {
	Name string
	Args []Expr
}

// DeallocateStmt is DEALLOCATE (spec 12 §9.2).
type DeallocateStmt struct {
	Name string // "" with All set means DEALLOCATE ALL
	All  bool
}

// ExplainStmt is EXPLAIN / EXPLAIN ANALYZE (spec 12 §10).
type ExplainStmt struct {
	Analyze bool
	Options map[string]Expr
	Body    Stmt
}

// ProfileStmt is PROFILE (spec 12 §10.3).
type ProfileStmt struct{ Body Stmt }

// PragmaStmt is PRAGMA (spec 12 §12).
type PragmaStmt struct {
	Scope string // qualifier before the dot, "" if none
	Name  string
	Value Expr // for `= value` or `(value)`; nil for a bare read
}

// SetStmt is SET (spec 12 §11.4).
type SetStmt struct {
	Local   bool
	Session bool
	Name    string
	Default bool // SET x = DEFAULT
	Value   Expr
}

func (*SelectStmt) stmtNode()      {}
func (*CreateTableStmt) stmtNode() {}
func (*CreateIndexStmt) stmtNode() {}
func (*DropStmt) stmtNode()        {}
func (*AlterTableStmt) stmtNode()  {}
func (*InsertStmt) stmtNode()      {}
func (*DeleteStmt) stmtNode()      {}
func (*UpdateStmt) stmtNode()      {}
func (*CopyStmt) stmtNode()        {}
func (*TxnStmt) stmtNode()         {}
func (*PrepareStmt) stmtNode()     {}
func (*ExecuteStmt) stmtNode()     {}
func (*DeallocateStmt) stmtNode()  {}
func (*ExplainStmt) stmtNode()     {}
func (*ProfileStmt) stmtNode()     {}
func (*PragmaStmt) stmtNode()      {}
func (*SetStmt) stmtNode()         {}
