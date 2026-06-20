package vectorsql

import (
	"strconv"
	"strings"
)

// parser is the hand-written recursive-descent parser of spec 12 §17.1. It consumes
// the token stream from the lexer and produces an AST. The grammar is LL(1) with at
// most two tokens of lookahead, so the parser keeps a cursor over the token slice and
// peeks ahead where the grammar needs it.
type parser struct {
	toks []Token
	pos  int
	err  *VecError
}

// Parse parses a single VectorSQL statement, with an optional trailing semicolon, and
// returns its AST (spec 12 §17.1). Trailing tokens after the statement are an error.
func Parse(src string) (Stmt, error) {
	toks, lerr := lex(src)
	if lerr != nil {
		return nil, lerr
	}
	p := &parser{toks: toks}
	stmt := p.parseStatement()
	if p.err != nil {
		return nil, p.err
	}
	p.accept(tokSemicolon)
	if p.cur().Kind != tokEOF {
		return nil, parseError(p.cur().Offset, "unexpected trailing tokens", "a single statement was expected")
	}
	return stmt, nil
}

// --- cursor primitives ---

func (p *parser) cur() Token  { return p.toks[p.pos] }
func (p *parser) peek() Token { return p.toks[min(p.pos+1, len(p.toks)-1)] }

func (p *parser) advance() Token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

// isKw reports whether the current token is the given keyword.
func (p *parser) isKw(kw string) bool {
	t := p.cur()
	return t.Kind == tokKeyword && t.Text == kw
}

// acceptKw consumes the current token if it is the given keyword.
func (p *parser) acceptKw(kw string) bool {
	if p.isKw(kw) {
		p.advance()
		return true
	}
	return false
}

// accept consumes the current token if it has the given kind.
func (p *parser) accept(k tokenKind) bool {
	if p.cur().Kind == k {
		p.advance()
		return true
	}
	return false
}

// expectKw requires the given keyword.
func (p *parser) expectKw(kw string) {
	if !p.acceptKw(kw) {
		p.fail("expected keyword " + strings.ToUpper(kw))
	}
}

// expect requires the given token kind, returning the consumed token.
func (p *parser) expect(k tokenKind, what string) Token {
	if p.cur().Kind != k {
		p.fail("expected " + what)
		return Token{}
	}
	return p.advance()
}

func (p *parser) fail(message string) {
	if p.err == nil {
		p.err = parseError(p.cur().Offset, message+", got "+describe(p.cur()), "")
	}
}

func (p *parser) failUnsupported(what, detail string) {
	if p.err == nil {
		p.err = unsupportedError(what, detail)
	}
}

func describe(t Token) string {
	switch t.Kind {
	case tokEOF:
		return "end of input"
	case tokKeyword:
		return "keyword '" + strings.ToUpper(t.Text) + "'"
	case tokIdent:
		return "identifier '" + t.Text + "'"
	case tokString:
		return "string"
	case tokInt, tokFloat:
		return "number"
	case tokParam:
		return "parameter"
	default:
		return "'" + t.Text + "'"
	}
}

// identText returns the identifier or keyword text at the cursor, consuming it. A
// keyword in an identifier position is rejected; the user must quote it.
func (p *parser) ident(what string) string {
	t := p.cur()
	if t.Kind == tokIdent {
		p.advance()
		return t.Text
	}
	p.fail("expected " + what)
	return ""
}

// --- statement dispatch ---

func (p *parser) parseStatement() Stmt {
	t := p.cur()
	if t.Kind != tokKeyword {
		p.fail("expected a statement keyword")
		return nil
	}
	switch t.Text {
	case "select":
		return p.parseSelect()
	case "insert":
		return p.parseInsert()
	case "upsert":
		p.failUnsupported("UPSERT keyword form not supported", "use INSERT ... ON CONFLICT (spec 12 §4.2)")
		return nil
	case "delete":
		return p.parseDelete()
	case "update":
		return p.parseUpdate()
	case "copy":
		return p.parseCopy()
	case "create":
		return p.parseCreate()
	case "drop":
		return p.parseDrop()
	case "alter":
		return p.parseAlter()
	case "begin", "commit", "rollback", "savepoint", "release":
		return p.parseTxn()
	case "prepare":
		return p.parsePrepare()
	case "execute":
		return p.parseExecute()
	case "deallocate":
		return p.parseDeallocate()
	case "explain":
		return p.parseExplain()
	case "profile":
		p.advance()
		return &ProfileStmt{Body: p.parseStatement()}
	case "pragma":
		return p.parsePragma()
	case "set":
		return p.parseSet()
	default:
		if t.Text == "join" || t.Text == "cross" {
			p.failUnsupported("JOIN is not supported", "VectorSQL has one FROM table per query (spec 12 §1.2)")
			return nil
		}
		p.fail("unexpected statement keyword '" + strings.ToUpper(t.Text) + "'")
		return nil
	}
}

// --- SELECT ---

func (p *parser) parseSelect() *SelectStmt {
	p.expectKw("select")
	s := &SelectStmt{}
	if p.acceptKw("distinct") {
		s.Distinct = true
		if p.acceptKw("on") {
			p.expect(tokLParen, "'('")
			s.DistinctOn = p.exprList()
			p.expect(tokRParen, "')'")
		}
	}
	p.parseSelectList(s)
	p.expectKw("from")
	s.From = p.qualifiedName()
	s.FromAlias = p.optionalAlias()
	if p.isKw("join") || p.isKw("cross") || p.isKw("inner") || p.isKw("left") || p.isKw("right") || p.isKw("full") || p.isKw("natural") {
		p.failUnsupported("JOIN is not supported; VectorSQL allows a single FROM table", "spec 12 §17.1 restricts SELECT to one table")
	}
	if p.cur().Kind == tokComma {
		p.failUnsupported("comma joins are not supported; VectorSQL allows a single FROM table", "spec 12 §17.1 restricts SELECT to one table")
	}
	if p.acceptKw("where") {
		s.Where = p.parseExpr()
	}
	if p.acceptKw("group") {
		p.expectKw("by")
		s.GroupBy = p.exprList()
	}
	if p.acceptKw("having") {
		s.Having = p.parseExpr()
	}
	if p.isKw("fusion") {
		s.Fusion = p.parseFusion()
	}
	if p.acceptKw("order") {
		p.expectKw("by")
		s.OrderBy = p.orderByList()
	}
	if p.acceptKw("limit") {
		s.Limit = p.parseExpr()
	}
	if p.acceptKw("offset") {
		s.Offset = p.parseExpr()
	}
	return s
}

func (p *parser) parseSelectList(s *SelectStmt) {
	if p.accept(tokStar) {
		s.Star = true
		return
	}
	for {
		e := p.parseExpr()
		alias := ""
		if p.acceptKw("as") {
			alias = p.ident("an alias")
		} else if p.cur().Kind == tokIdent {
			alias = p.advance().Text
		}
		s.Columns = append(s.Columns, ExprAlias{Expr: e, Alias: alias})
		if !p.accept(tokComma) {
			break
		}
	}
}

func (p *parser) optionalAlias() string {
	if p.acceptKw("as") {
		return p.ident("an alias")
	}
	if p.cur().Kind == tokIdent {
		return p.advance().Text
	}
	return ""
}

func (p *parser) orderByList() []OrderItem {
	var items []OrderItem
	for {
		it := OrderItem{Expr: p.parseExpr()}
		if p.acceptKw("asc") {
			it.Desc = false
		} else if p.acceptKw("desc") {
			it.Desc = true
		}
		if p.acceptKw("nulls") {
			it.NullsSet = true
			if p.acceptKw("first") {
				it.NullsFirst = true
			} else {
				p.expectKw("last")
			}
		}
		items = append(items, it)
		if !p.accept(tokComma) {
			break
		}
	}
	return items
}

// --- FUSION ---

func (p *parser) parseFusion() *FusionClause {
	p.expectKw("fusion")
	p.expect(tokLParen, "'('")
	fc := &FusionClause{}
	for {
		fc.Streams = append(fc.Streams, p.parseFusionStream())
		if !p.accept(tokComma) {
			break
		}
	}
	p.expect(tokRParen, "')'")
	if p.acceptKw("with") {
		fc.Options = p.optionMap()
	}
	return fc
}

func (p *parser) parseFusionStream() FusionStream {
	if p.acceptKw("keyword") {
		p.expectKw("match")
		p.expect(tokLParen, "'('")
		cols := p.identList()
		p.expect(tokRParen, "')'")
		p.expectKw("against")
		p.expect(tokLParen, "'('")
		against := p.parseExpr()
		p.expect(tokRParen, "')'")
		fs := FusionStream{Keyword: true, MatchCols: cols, Against: against}
		if p.acceptKw("with") {
			fs.Options = p.optionMap()
		}
		return fs
	}
	// VECTOR ORDER BY col <op> query
	p.expectKw("vector")
	p.expectKw("order")
	p.expectKw("by")
	col := p.ident("a vector column")
	op, ok := p.distanceOp()
	if !ok {
		p.fail("expected a distance operator")
	}
	return FusionStream{Column: col, Op: op, Query: p.parseExpr()}
}

func (p *parser) distanceOp() (DistanceOp, bool) {
	switch p.cur().Kind {
	case tokL2:
		p.advance()
		return OpL2Distance, true
	case tokCos:
		p.advance()
		return OpCosineDistance, true
	case tokIP:
		p.advance()
		return OpNegInnerProd, true
	case tokL1:
		p.advance()
		return OpL1Distance, true
	default:
		return 0, false
	}
}

// --- INSERT / UPSERT ---

func (p *parser) parseInsert() Stmt {
	p.expectKw("insert")
	p.expectKw("into")
	st := &InsertStmt{Table: p.qualifiedName()}
	if p.accept(tokLParen) {
		st.Columns = p.identList()
		p.expect(tokRParen, "')'")
	}
	p.expectKw("values")
	for {
		p.expect(tokLParen, "'('")
		st.Rows = append(st.Rows, p.exprList())
		p.expect(tokRParen, "')'")
		if !p.accept(tokComma) {
			break
		}
	}
	if p.acceptKw("on") {
		p.expectKw("conflict")
		st.OnConflict = p.parseOnConflict()
	}
	p.parseReturning(&st.Returning, &st.ReturnAll)
	return st
}

func (p *parser) parseOnConflict() *OnConflict {
	oc := &OnConflict{}
	if p.accept(tokLParen) {
		oc.Columns = p.identList()
		p.expect(tokRParen, "')'")
	}
	p.expectKw("do")
	if p.acceptKw("nothing") {
		oc.DoNothing = true
		return oc
	}
	p.expectKw("update")
	p.expectKw("set")
	oc.Assigns = p.assignList()
	return oc
}

func (p *parser) parseDelete() *DeleteStmt {
	p.expectKw("delete")
	p.expectKw("from")
	st := &DeleteStmt{Table: p.qualifiedName()}
	if p.acceptKw("where") {
		st.Where = p.parseExpr()
	}
	p.parseReturning(&st.Returning, &st.ReturnAll)
	return st
}

func (p *parser) parseUpdate() *UpdateStmt {
	p.expectKw("update")
	st := &UpdateStmt{Table: p.qualifiedName()}
	p.expectKw("set")
	st.Assigns = p.assignList()
	if p.acceptKw("where") {
		st.Where = p.parseExpr()
	}
	p.parseReturning(&st.Returning, &st.ReturnAll)
	return st
}

func (p *parser) assignList() []Assignment {
	var out []Assignment
	for {
		col := p.ident("a column name")
		p.expect(tokEq, "'='")
		out = append(out, Assignment{Column: col, Value: p.parseExpr()})
		if !p.accept(tokComma) {
			break
		}
	}
	return out
}

func (p *parser) parseReturning(items *[]ExprAlias, all *bool) {
	if !p.acceptKw("returning") {
		return
	}
	if p.accept(tokStar) {
		*all = true
		return
	}
	for {
		e := p.parseExpr()
		alias := ""
		if p.acceptKw("as") {
			alias = p.ident("an alias")
		} else if p.cur().Kind == tokIdent {
			alias = p.advance().Text
		}
		*items = append(*items, ExprAlias{Expr: e, Alias: alias})
		if !p.accept(tokComma) {
			break
		}
	}
}

func (p *parser) parseCopy() *CopyStmt {
	p.expectKw("copy")
	st := &CopyStmt{Table: p.qualifiedName()}
	if p.accept(tokLParen) {
		st.Columns = p.identList()
		p.expect(tokRParen, "')'")
	}
	p.expectKw("from")
	if p.acceptKw("stdin") {
		st.Stdin = true
	} else {
		st.Source = p.expect(tokString, "a file path string").Text
	}
	if p.acceptKw("format") {
		st.Format = p.formatWord()
	}
	if p.acceptKw("with") {
		st.Options = p.optionMap()
	}
	return st
}

func (p *parser) formatWord() string {
	t := p.cur()
	if t.Kind == tokIdent || t.Kind == tokKeyword {
		p.advance()
		return strings.ToLower(t.Text)
	}
	p.fail("expected a format name")
	return ""
}

// --- DDL ---

func (p *parser) parseCreate() Stmt {
	p.expectKw("create")
	if p.acceptKw("table") {
		return p.parseCreateTable()
	}
	unique := p.acceptKw("unique")
	if p.acceptKw("index") {
		return p.parseCreateIndex(unique)
	}
	p.fail("expected TABLE or INDEX after CREATE")
	return nil
}

func (p *parser) parseCreateTable() *CreateTableStmt {
	st := &CreateTableStmt{}
	if p.acceptKw("if") {
		p.expectKw("not")
		p.expectKw("exists")
		st.IfNotExists = true
	}
	st.Name = p.qualifiedName()
	p.expect(tokLParen, "'('")
	for {
		if p.parseTableConstraint(st) {
			// constraint consumed
		} else {
			st.Columns = append(st.Columns, p.parseColumnSpec())
		}
		if !p.accept(tokComma) {
			break
		}
	}
	p.expect(tokRParen, "')'")
	return st
}

func (p *parser) parseTableConstraint(st *CreateTableStmt) bool {
	switch {
	case p.isKw("primary"):
		p.advance()
		p.expectKw("key")
		p.expect(tokLParen, "'('")
		st.PrimaryKey = p.identList()
		p.expect(tokRParen, "')'")
		return true
	case p.isKw("unique"):
		p.advance()
		p.expect(tokLParen, "'('")
		st.Uniques = append(st.Uniques, p.identList())
		p.expect(tokRParen, "')'")
		return true
	case p.isKw("check"):
		p.advance()
		p.expect(tokLParen, "'('")
		st.Checks = append(st.Checks, p.parseExpr())
		p.expect(tokRParen, "')'")
		return true
	default:
		return false
	}
}

func (p *parser) parseColumnSpec() ColumnSpec {
	c := ColumnSpec{Name: p.ident("a column name")}
	c.Type = p.parseType()
	for {
		switch {
		case p.isKw("not"):
			p.advance()
			p.expectKw("null")
			c.NotNull = true
		case p.isKw("null"):
			p.advance()
			c.Nullable = true
		case p.isKw("default"):
			p.advance()
			c.Default = p.parseExpr()
		case p.isKw("primary"):
			p.advance()
			p.expectKw("key")
			c.PrimaryKey = true
		case p.isKw("unique"):
			p.advance()
			c.Unique = true
		default:
			return c
		}
	}
}

// parseType reads a column type with an optional integer argument (spec 12 §3.1.1).
// Two-word forms (DOUBLE PRECISION) are folded to their canonical single name.
func (p *parser) parseType() *TypeRef {
	t := p.cur()
	if t.Kind != tokIdent && t.Kind != tokKeyword {
		p.fail("expected a column type")
		return nil
	}
	name := strings.ToLower(t.Text)
	p.advance()
	if name == "double" && p.cur().Kind == tokIdent && p.cur().Text == "precision" {
		p.advance()
		name = "double precision"
	}
	tr := &TypeRef{Name: name}
	if p.accept(tokLParen) {
		n := p.expect(tokInt, "an integer dimension").Text
		v, _ := strconv.Atoi(n)
		tr.Arg = v
		tr.HasArg = true
		p.expect(tokRParen, "')'")
	}
	return tr
}

func (p *parser) parseCreateIndex(unique bool) *CreateIndexStmt {
	st := &CreateIndexStmt{Unique: unique}
	if p.acceptKw("if") {
		p.expectKw("not")
		p.expectKw("exists")
		st.IfNotExists = true
	}
	if p.cur().Kind == tokIdent {
		st.Name = p.advance().Text
	}
	p.expectKw("on")
	st.Table = p.qualifiedName()
	p.expectKw("using")
	st.IndexType = p.contextualWord("an index type")
	p.expect(tokLParen, "'('")
	st.Column = p.ident("the indexed column")
	if p.cur().Kind == tokIdent {
		st.Opclass = p.advance().Text
	}
	p.expect(tokRParen, "')'")
	if p.acceptKw("with") {
		st.Options = p.optionMap()
	}
	if p.acceptKw("where") {
		st.Where = p.parseExpr()
	}
	return st
}

// contextualWord reads an identifier or keyword used as a contextual name (index type,
// opclass), per spec 12 §17.4.
func (p *parser) contextualWord(what string) string {
	t := p.cur()
	if t.Kind == tokIdent || t.Kind == tokKeyword {
		p.advance()
		return strings.ToLower(t.Text)
	}
	p.fail("expected " + what)
	return ""
}

func (p *parser) parseDrop() *DropStmt {
	p.expectKw("drop")
	st := &DropStmt{}
	if p.acceptKw("table") {
		st.Index = false
	} else if p.acceptKw("index") {
		st.Index = true
	} else {
		p.fail("expected TABLE or INDEX after DROP")
		return st
	}
	if p.acceptKw("if") {
		p.expectKw("exists")
		st.IfExists = true
	}
	if st.Index && p.acceptKw("on") {
		st.OnTable = p.qualifiedName()
		p.expect(tokLParen, "'('")
		st.OnColumn = p.ident("a column name")
		p.expect(tokRParen, "')'")
		return st
	}
	st.Name = p.qualifiedName()
	return st
}

func (p *parser) parseAlter() *AlterTableStmt {
	p.expectKw("alter")
	p.expectKw("table")
	st := &AlterTableStmt{Table: p.qualifiedName()}
	switch {
	case p.acceptKw("add"):
		p.acceptKw("column")
		c := p.parseColumnSpec()
		st.Action = "add_column"
		st.Column = &c
	case p.acceptKw("drop"):
		p.acceptKw("column")
		st.Action = "drop_column"
		st.DropCol = p.ident("a column name")
	case p.acceptKw("rename"):
		if p.acceptKw("to") {
			st.Action = "rename_table"
			st.NewName = p.ident("a new table name")
		} else {
			p.acceptKw("column")
			st.Action = "rename_column"
			st.OldColumn = p.ident("a column name")
			p.expectKw("to")
			st.NewName = p.ident("a new column name")
		}
	default:
		p.fail("expected ADD, DROP, or RENAME")
	}
	return st
}

// --- transactions ---

func (p *parser) parseTxn() *TxnStmt {
	t := p.advance()
	st := &TxnStmt{Kind: t.Text}
	switch t.Text {
	case "begin":
		p.acceptKw("transaction")
		if p.acceptKw("isolation") {
			p.expectKw("level")
			st.Isolation = p.isolationLevel()
		}
	case "commit":
		p.acceptKw("transaction")
	case "rollback":
		p.acceptKw("transaction")
		if p.acceptKw("to") {
			p.acceptKw("savepoint")
			st.Kind = "rollback_to"
			st.Name = p.ident("a savepoint name")
		}
	case "savepoint":
		st.Name = p.ident("a savepoint name")
	case "release":
		p.acceptKw("savepoint")
		st.Name = p.ident("a savepoint name")
	}
	return st
}

func (p *parser) isolationLevel() string {
	switch {
	case p.acceptKw("serializable"):
		return "serializable"
	case p.isKw("repeatable"):
		p.advance()
		p.ident("read")
		return "repeatable read"
	case p.isKw("read"):
		p.advance()
		w := p.ident("committed or uncommitted")
		return "read " + w
	default:
		p.fail("expected an isolation level")
		return ""
	}
}

// --- prepared statements ---

func (p *parser) parsePrepare() *PrepareStmt {
	p.expectKw("prepare")
	st := &PrepareStmt{Name: p.ident("a statement name")}
	if p.accept(tokLParen) {
		for {
			st.Types = append(st.Types, p.parseType())
			if !p.accept(tokComma) {
				break
			}
		}
		p.expect(tokRParen, "')'")
	}
	p.expectKw("as")
	st.Body = p.parseStatement()
	return st
}

func (p *parser) parseExecute() *ExecuteStmt {
	p.expectKw("execute")
	st := &ExecuteStmt{Name: p.ident("a statement name")}
	if p.accept(tokLParen) {
		st.Args = p.exprList()
		p.expect(tokRParen, "')'")
	}
	return st
}

func (p *parser) parseDeallocate() *DeallocateStmt {
	p.expectKw("deallocate")
	p.acceptKw("prepare")
	st := &DeallocateStmt{}
	if p.acceptKw("all") {
		st.All = true
		return st
	}
	st.Name = p.ident("a statement name")
	return st
}

// --- EXPLAIN / PRAGMA / SET ---

func (p *parser) parseExplain() *ExplainStmt {
	p.expectKw("explain")
	st := &ExplainStmt{}
	st.Analyze = p.acceptKw("analyze")
	if p.accept(tokLParen) {
		st.Options = p.optionMap()
	}
	st.Body = p.parseStatement()
	return st
}

func (p *parser) parsePragma() *PragmaStmt {
	p.expectKw("pragma")
	st := &PragmaStmt{}
	name := p.ident("a pragma name")
	if p.accept(tokDot) {
		st.Scope = name
		st.Name = p.ident("a pragma name")
	} else {
		st.Name = name
	}
	switch {
	case p.accept(tokEq):
		st.Value = p.parseExpr()
	case p.accept(tokLParen):
		st.Value = p.parseExpr()
		p.expect(tokRParen, "')'")
	}
	return st
}

func (p *parser) parseSet() *SetStmt {
	p.expectKw("set")
	st := &SetStmt{}
	if p.acceptKw("local") {
		st.Local = true
	} else if p.acceptKw("session") {
		st.Session = true
	}
	st.Name = p.ident("a setting name")
	if !p.accept(tokEq) {
		p.expectKw("to")
	}
	if p.acceptKw("default") {
		st.Default = true
	} else {
		st.Value = p.parseExpr()
	}
	return st
}

// --- shared helpers ---

// qualifiedName reads an optionally schema-qualified name and returns the final part.
func (p *parser) qualifiedName() string {
	name := p.ident("a name")
	if p.accept(tokDot) {
		name = p.ident("a name")
	}
	return name
}

func (p *parser) identList() []string {
	var out []string
	for {
		out = append(out, p.ident("a column name"))
		if !p.accept(tokComma) {
			break
		}
	}
	return out
}

func (p *parser) exprList() []Expr {
	var out []Expr
	for {
		out = append(out, p.parseExpr())
		if !p.accept(tokComma) {
			break
		}
	}
	return out
}

// optionMap reads `(ident = value, ...)` and returns a name→expr map (spec 12 §3.2).
func (p *parser) optionMap() map[string]Expr {
	p.expect(tokLParen, "'('")
	m := map[string]Expr{}
	for {
		k := p.contextualWord("an option name")
		p.expect(tokEq, "'='")
		m[k] = p.parseExpr()
		if !p.accept(tokComma) {
			break
		}
	}
	p.expect(tokRParen, "')'")
	return m
}
