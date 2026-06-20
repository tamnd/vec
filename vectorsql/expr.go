package vectorsql

import (
	"strconv"
	"strings"
)

// This file holds the expression grammar (spec 12 §15 Expr production). It is a
// precedence-climbing parser layered over the recursive-descent statement parser.
// Precedence, lowest to highest, follows PostgreSQL: OR, AND, NOT, comparison and the
// postfix predicates (IS NULL / BETWEEN / IN / LIKE), distance operators, additive,
// multiplicative, the prefix sign, then postfix (:: cast, -> json), then primaries.

func (p *parser) parseExpr() Expr { return p.parseOr() }

func (p *parser) parseOr() Expr {
	left := p.parseAnd()
	for p.acceptKw("or") {
		left = &BinaryExpr{Op: "or", Left: left, Right: p.parseAnd()}
	}
	return left
}

func (p *parser) parseAnd() Expr {
	left := p.parseNot()
	for p.acceptKw("and") {
		left = &BinaryExpr{Op: "and", Left: left, Right: p.parseNot()}
	}
	return left
}

func (p *parser) parseNot() Expr {
	if p.acceptKw("not") {
		return &UnaryExpr{Op: "not", Expr: p.parseNot()}
	}
	return p.parsePredicate()
}

// parsePredicate parses a comparison and the postfix predicate forms that bind at the
// same level: IS [NOT] NULL, [NOT] BETWEEN, [NOT] IN, [NOT] LIKE/ILIKE.
func (p *parser) parsePredicate() Expr {
	left := p.parseComparison()
	for {
		switch {
		case p.isKw("is"):
			p.advance()
			not := p.acceptKw("not")
			p.expectKw("null")
			left = &IsNullExpr{Expr: left, Not: not}
		case p.isKw("not") && p.peekKw("between"):
			p.advance()
			left = p.parseBetween(left, true)
		case p.isKw("between"):
			left = p.parseBetween(left, false)
		case p.isKw("not") && p.peekKw("in"):
			p.advance()
			left = p.parseIn(left, true)
		case p.isKw("in"):
			left = p.parseIn(left, false)
		case p.isKw("not") && (p.peekKw("like") || p.peekKw("ilike")):
			p.advance()
			left = p.parseLike(left, true)
		case p.isKw("like") || p.isKw("ilike"):
			left = p.parseLike(left, false)
		default:
			return left
		}
	}
}

func (p *parser) peekKw(kw string) bool {
	t := p.peek()
	return t.Kind == tokKeyword && t.Text == kw
}

func (p *parser) parseBetween(left Expr, not bool) Expr {
	p.expectKw("between")
	lo := p.parseDistance()
	p.expectKw("and")
	hi := p.parseDistance()
	return &BetweenExpr{Expr: left, Lo: lo, Hi: hi, Not: not}
}

func (p *parser) parseIn(left Expr, not bool) Expr {
	p.expectKw("in")
	p.expect(tokLParen, "'('")
	ie := &InExpr{Expr: left, Not: not}
	if p.isKw("select") {
		ie.Sub = p.parseSelect()
	} else {
		ie.List = p.exprList()
	}
	p.expect(tokRParen, "')'")
	return ie
}

func (p *parser) parseLike(left Expr, not bool) Expr {
	insens := p.acceptKw("ilike")
	if !insens {
		p.expectKw("like")
	}
	return &LikeExpr{Expr: left, Pattern: p.parseDistance(), Not: not, Insens: insens}
}

func (p *parser) parseComparison() Expr {
	left := p.parseDistance()
	if op, ok := p.comparisonOp(); ok {
		return &BinaryExpr{Op: op, Left: left, Right: p.parseDistance()}
	}
	return left
}

func (p *parser) comparisonOp() (string, bool) {
	switch p.cur().Kind {
	case tokEq:
		p.advance()
		return "=", true
	case tokNe:
		p.advance()
		return "<>", true
	case tokLt:
		p.advance()
		return "<", true
	case tokLe:
		p.advance()
		return "<=", true
	case tokGt:
		p.advance()
		return ">", true
	case tokGe:
		p.advance()
		return ">=", true
	case tokContains:
		p.advance()
		return "@>", true
	case tokContained:
		p.advance()
		return "<@", true
	case tokOverlap:
		p.advance()
		return "&&", true
	default:
		return "", false
	}
}

// parseDistance handles the vector distance operators, which bind tighter than
// comparison but looser than arithmetic (spec 12 §6.3).
func (p *parser) parseDistance() Expr {
	left := p.parseAdditive()
	for {
		op, ok := p.distanceOp()
		if !ok {
			return left
		}
		left = &DistanceExpr{Op: op, Left: left, Right: p.parseAdditive()}
	}
}

func (p *parser) parseAdditive() Expr {
	left := p.parseMultiplicative()
	for {
		switch p.cur().Kind {
		case tokPlus:
			p.advance()
			left = &BinaryExpr{Op: "+", Left: left, Right: p.parseMultiplicative()}
		case tokMinus:
			p.advance()
			left = &BinaryExpr{Op: "-", Left: left, Right: p.parseMultiplicative()}
		case tokConcat:
			p.advance()
			left = &BinaryExpr{Op: "||", Left: left, Right: p.parseMultiplicative()}
		default:
			return left
		}
	}
}

func (p *parser) parseMultiplicative() Expr {
	left := p.parseUnary()
	for {
		switch p.cur().Kind {
		case tokStar:
			p.advance()
			left = &BinaryExpr{Op: "*", Left: left, Right: p.parseUnary()}
		case tokSlash:
			p.advance()
			left = &BinaryExpr{Op: "/", Left: left, Right: p.parseUnary()}
		case tokPercent:
			p.advance()
			left = &BinaryExpr{Op: "%", Left: left, Right: p.parseUnary()}
		default:
			return left
		}
	}
}

func (p *parser) parseUnary() Expr {
	if p.cur().Kind == tokMinus {
		p.advance()
		return &UnaryExpr{Op: "-", Expr: p.parseUnary()}
	}
	return p.parsePostfix()
}

// parsePostfix handles the postfix :: cast and -> / ->> json access operators.
func (p *parser) parsePostfix() Expr {
	e := p.parsePrimary()
	for {
		switch p.cur().Kind {
		case tokCast:
			p.advance()
			e = &CastExpr{Expr: e, Type: p.parseType()}
		case tokArrow:
			p.advance()
			e = &JSONExpr{Expr: e, Key: p.expect(tokString, "a JSON key string").Text, Text: false}
		case tokArrow2:
			p.advance()
			e = &JSONExpr{Expr: e, Key: p.expect(tokString, "a JSON key string").Text, Text: true}
		default:
			return e
		}
	}
}

func (p *parser) parsePrimary() Expr {
	t := p.cur()
	switch t.Kind {
	case tokInt:
		p.advance()
		return p.intLiteral(t)
	case tokFloat:
		p.advance()
		v, err := strconv.ParseFloat(t.Text, 64)
		if err != nil {
			p.fail("invalid float literal")
		}
		return &FloatLit{Value: v, Offset: t.Offset}
	case tokString:
		p.advance()
		return &StringLit{Value: t.Text, Offset: t.Offset}
	case tokParam:
		p.advance()
		return &ParamRef{Name: t.ParamName, Pos: t.ParamPos, Offset: t.Offset}
	case tokLParen:
		return p.parseParenOrSubquery()
	case tokStar:
		p.advance()
		return &Star{Offset: t.Offset}
	case tokKeyword:
		return p.parseKeywordPrimary(t)
	case tokIdent:
		return p.parseIdentPrimary(t)
	default:
		p.fail("expected an expression")
		return &NullLit{Offset: t.Offset}
	}
}

func (p *parser) intLiteral(t Token) Expr {
	v, err := parseIntLiteral(t.Text)
	if err != nil {
		p.fail("invalid integer literal")
	}
	return &IntLit{Value: v, Offset: t.Offset}
}

// parseIntLiteral parses a decimal, hex, binary, or octal integer (spec 12 §2.5.1).
func parseIntLiteral(s string) (int64, error) {
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "0x"):
		return strconv.ParseInt(low[2:], 16, 64)
	case strings.HasPrefix(low, "0b"):
		return strconv.ParseInt(low[2:], 2, 64)
	case strings.HasPrefix(low, "0o"):
		return strconv.ParseInt(low[2:], 8, 64)
	default:
		return strconv.ParseInt(s, 10, 64)
	}
}

func (p *parser) parseParenOrSubquery() Expr {
	p.expect(tokLParen, "'('")
	if p.isKw("select") {
		sub := p.parseSelect()
		p.expect(tokRParen, "')'")
		// A scalar subquery is represented as an InExpr-free wrapper; the binder
		// rejects subqueries outside IN/EXISTS in M6, so carry it minimally.
		return &subqueryExpr{Sub: sub}
	}
	e := p.parseExpr()
	p.expect(tokRParen, "')'")
	return e
}

func (p *parser) parseKeywordPrimary(t Token) Expr {
	switch t.Text {
	case "true":
		p.advance()
		return &BoolLit{Value: true, Offset: t.Offset}
	case "false":
		p.advance()
		return &BoolLit{Value: false, Offset: t.Offset}
	case "null":
		p.advance()
		return &NullLit{Offset: t.Offset}
	case "case":
		return p.parseCase()
	case "exists":
		p.advance()
		p.expect(tokLParen, "'('")
		sub := p.parseSelect()
		p.expect(tokRParen, "')'")
		return &existsExpr{Sub: sub}
	case "fusion":
		p.fail("FUSION is a reserved clause keyword; quote \"fusion\" to use it as a column")
		return &NullLit{Offset: t.Offset}
	default:
		p.fail("unexpected keyword '" + strings.ToUpper(t.Text) + "' in an expression")
		return &NullLit{Offset: t.Offset}
	}
}

// parseIdentPrimary parses a column reference or a function call. A '(' immediately
// after the identifier makes it a function call.
func (p *parser) parseIdentPrimary(t Token) Expr {
	p.advance()
	if p.cur().Kind == tokLParen {
		return p.parseFuncCall(t.Text, t.Offset)
	}
	ref := &ColumnRef{Name: t.Text, Offset: t.Offset}
	if p.accept(tokDot) {
		if p.accept(tokStar) {
			// table.* in a function arg position; represent as a qualified star.
			return &Star{Offset: t.Offset}
		}
		ref.Table = t.Text
		ref.Name = p.ident("a column name")
	}
	return ref
}

func (p *parser) parseFuncCall(name string, off int) Expr {
	p.expect(tokLParen, "'('")
	fc := &FuncCall{Name: name, Offset: off}
	if p.accept(tokStar) {
		fc.Star = true
		p.expect(tokRParen, "')'")
	} else if p.accept(tokRParen) {
		// no args
	} else {
		fc.Distinct = p.acceptKw("distinct")
		fc.Args = p.exprList()
		p.expect(tokRParen, "')'")
	}
	if p.acceptKw("filter") {
		p.expect(tokLParen, "'('")
		p.expectKw("where")
		fc.Filter = p.parseExpr()
		p.expect(tokRParen, "')'")
	}
	return fc
}

func (p *parser) parseCase() Expr {
	p.expectKw("case")
	ce := &CaseExpr{}
	if !p.isKw("when") {
		ce.Operand = p.parseExpr()
	}
	for p.acceptKw("when") {
		when := p.parseExpr()
		p.expectKw("then")
		ce.Whens = append(ce.Whens, WhenClause{When: when, Then: p.parseExpr()})
	}
	if p.acceptKw("else") {
		ce.Else = p.parseExpr()
	}
	p.expectKw("end")
	return ce
}

// subqueryExpr and existsExpr carry parenthesized SELECTs that the binder handles in
// IN / EXISTS positions and rejects elsewhere in M6 (spec 12 §8.4).
type subqueryExpr struct{ Sub *SelectStmt }
type existsExpr struct{ Sub *SelectStmt }

func (*subqueryExpr) exprNode() {}
func (*existsExpr) exprNode()   {}
