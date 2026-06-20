package vectorsql

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// lexer scans VectorSQL source into a token stream (spec 12 §2). It is UTF-8 aware:
// string literals and double-quoted identifiers may hold any UTF-8, while unquoted
// identifiers and keywords are ASCII only.
type lexer struct {
	src    string
	pos    int     // byte offset of the next unread rune
	tokens []Token // accumulated output
	err    *VecError
}

// lex tokenizes the whole source, returning the token slice or the first lexical
// error. A trailing tokEOF always terminates the slice on success.
func lex(src string) ([]Token, *VecError) {
	l := &lexer{src: src}
	l.run()
	if l.err != nil {
		return nil, l.err
	}
	l.tokens = append(l.tokens, Token{Kind: tokEOF, Offset: len(src)})
	return l.tokens, nil
}

func (l *lexer) run() {
	for l.pos < len(l.src) && l.err == nil {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.pos++
		case c == '-' && l.peekAt(1) == '-':
			l.skipLineComment()
		case c == '/' && l.peekAt(1) == '*':
			l.skipBlockComment()
		case c == '\'':
			l.scanString()
		case c == '"':
			l.scanQuotedIdent()
		case c == ':' && l.peekAt(1) != ':':
			l.scanNamedParam()
		case c == '$':
			l.scanPositionalParam()
		case isDigit(c):
			l.scanNumber()
		case c == '.' && isDigit(l.peekAt(1)):
			l.scanNumber()
		case isIdentStart(c):
			l.scanWord()
		default:
			l.scanOperator()
		}
	}
}

func (l *lexer) peekAt(off int) byte {
	i := l.pos + off
	if i < len(l.src) {
		return l.src[i]
	}
	return 0
}

func (l *lexer) emit(kind tokenKind, text string, off int) {
	l.tokens = append(l.tokens, Token{Kind: kind, Text: text, Offset: off})
}

func (l *lexer) fail(off int, message, detail string) {
	if l.err == nil {
		l.err = parseError(off, message, detail)
	}
}

func (l *lexer) skipLineComment() {
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.pos++
	}
}

func (l *lexer) skipBlockComment() {
	start := l.pos
	l.pos += 2 // consume /*
	for l.pos < len(l.src) {
		if l.src[l.pos] == '/' && l.peekAt(1) == '*' {
			l.fail(l.pos, "nested block comment", "block comments may not be nested (spec 12 §2.2)")
			return
		}
		if l.src[l.pos] == '*' && l.peekAt(1) == '/' {
			l.pos += 2
			return
		}
		l.pos++
	}
	l.fail(start, "unterminated block comment", "")
}

// scanString reads a single-quoted string, handling ” doubling and backslash
// escapes (spec 12 §2.5.3). The decoded value (not the raw source) is the token text.
func (l *lexer) scanString() {
	start := l.pos
	l.pos++ // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '\'':
			if l.peekAt(1) == '\'' {
				b.WriteByte('\'')
				l.pos += 2
				continue
			}
			l.pos++ // closing quote
			l.emit(tokString, b.String(), start)
			return
		case '\\':
			if !l.scanEscape(&b) {
				return
			}
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
	l.fail(start, "unterminated string literal", "")
}

// scanEscape decodes one backslash escape into b, advancing past it. It returns false
// (after recording an error) on a malformed escape.
func (l *lexer) scanEscape(b *strings.Builder) bool {
	esc := l.peekAt(1)
	switch esc {
	case 'n':
		b.WriteByte('\n')
		l.pos += 2
	case 'r':
		b.WriteByte('\r')
		l.pos += 2
	case 't':
		b.WriteByte('\t')
		l.pos += 2
	case '\\':
		b.WriteByte('\\')
		l.pos += 2
	case '\'':
		b.WriteByte('\'')
		l.pos += 2
	case 'u':
		return l.scanUnicodeEscape(b, 4)
	case 'U':
		return l.scanUnicodeEscape(b, 8)
	default:
		l.fail(l.pos, "invalid escape sequence", "")
		return false
	}
	return true
}

func (l *lexer) scanUnicodeEscape(b *strings.Builder, digits int) bool {
	start := l.pos
	l.pos += 2 // consume \u or \U
	if l.pos+digits > len(l.src) {
		l.fail(start, "truncated unicode escape", "")
		return false
	}
	var r rune
	for i := 0; i < digits; i++ {
		d := hexVal(l.src[l.pos+i])
		if d < 0 {
			l.fail(start, "invalid unicode escape digit", "")
			return false
		}
		r = r<<4 | rune(d)
	}
	if !utf8.ValidRune(r) {
		l.fail(start, "invalid unicode code point", "")
		return false
	}
	b.WriteRune(r)
	l.pos += digits
	return true
}

// scanQuotedIdent reads a double-quoted, case-sensitive identifier with "" doubling
// (spec 12 §2.4). The decoded value is the token text; quoted idents keep their case.
func (l *lexer) scanQuotedIdent() {
	start := l.pos
	l.pos++ // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '"' {
			if l.peekAt(1) == '"' {
				b.WriteByte('"')
				l.pos += 2
				continue
			}
			l.pos++
			l.tokens = append(l.tokens, Token{Kind: tokIdent, Text: b.String(), Offset: start})
			return
		}
		b.WriteByte(c)
		l.pos++
	}
	l.fail(start, "unterminated quoted identifier", "")
}

func (l *lexer) scanNamedParam() {
	start := l.pos
	l.pos++ // consume ':'
	nameStart := l.pos
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.pos++
	}
	if l.pos == nameStart {
		l.fail(start, "empty named parameter", "expected an identifier after ':'")
		return
	}
	name := strings.ToLower(l.src[nameStart:l.pos])
	l.tokens = append(l.tokens, Token{Kind: tokParam, ParamName: name, Offset: start})
}

func (l *lexer) scanPositionalParam() {
	start := l.pos
	l.pos++ // consume '$'
	numStart := l.pos
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos == numStart {
		l.fail(start, "empty positional parameter", "expected a number after '$'")
		return
	}
	n := 0
	for _, c := range l.src[numStart:l.pos] {
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		l.fail(start, "positional parameter must be >= 1", "")
		return
	}
	l.tokens = append(l.tokens, Token{Kind: tokParam, ParamPos: n, Offset: start})
}

// scanNumber reads an integer or float literal, including hex/binary/octal integers
// and scientific-notation floats (spec 12 §2.5.1, §2.5.2).
func (l *lexer) scanNumber() {
	start := l.pos
	// Radix-prefixed integers.
	if l.src[l.pos] == '0' && l.pos+1 < len(l.src) {
		switch lower := l.src[l.pos+1] | 0x20; lower {
		case 'x', 'b', 'o':
			l.scanRadixInt(start, lower)
			return
		}
	}
	isFloat := false
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		isFloat = true
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		isFloat = true
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		expStart := l.pos
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
		if l.pos == expStart {
			l.fail(start, "exponent has no digits", "")
			return
		}
	}
	kind := tokInt
	if isFloat {
		kind = tokFloat
	}
	l.emit(kind, l.src[start:l.pos], start)
}

func (l *lexer) scanRadixInt(start int, radix byte) {
	l.pos += 2 // consume 0x/0b/0o
	digStart := l.pos
	valid := func(c byte) bool {
		switch radix {
		case 'x':
			return hexVal(c) >= 0
		case 'b':
			return c == '0' || c == '1'
		default: // 'o'
			return c >= '0' && c <= '7'
		}
	}
	for l.pos < len(l.src) && valid(l.src[l.pos]) {
		l.pos++
	}
	if l.pos == digStart {
		l.fail(start, "radix literal has no digits", "")
		return
	}
	l.emit(tokInt, l.src[start:l.pos], start)
}

// scanWord reads an unquoted keyword or identifier, ASCII only (spec 12 §2.4). A
// non-ASCII byte in this position is a lexical error.
func (l *lexer) scanWord() {
	start := l.pos
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.pos++
	}
	word := l.src[start:l.pos]
	if r, _ := utf8.DecodeRuneInString(l.src[l.pos:]); r != utf8.RuneError && r >= utf8.RuneSelf && !isBoundary(r) {
		l.fail(l.pos, "non-ASCII character in unquoted identifier", "quote the identifier to use non-ASCII characters")
		return
	}
	lower := strings.ToLower(word)
	if isKeyword(lower) {
		l.emit(tokKeyword, lower, start)
		return
	}
	l.emit(tokIdent, lower, start)
}

// scanOperator reads punctuation and multi-byte operators (spec 12 §2.7).
func (l *lexer) scanOperator() {
	start := l.pos
	c := l.src[l.pos]
	two := l.src[l.pos:min(l.pos+2, len(l.src))]
	three := l.src[l.pos:min(l.pos+3, len(l.src))]

	switch {
	case three == "<->":
		l.op(tokL2, 3, start)
	case three == "<=>":
		l.op(tokCos, 3, start)
	case three == "<#>":
		l.op(tokIP, 3, start)
	case three == "<+>":
		l.op(tokL1, 3, start)
	case three == "->>":
		l.op(tokArrow2, 3, start)
	case two == "->":
		l.op(tokArrow, 2, start)
	case two == "::":
		l.op(tokCast, 2, start)
	case two == "||":
		l.op(tokConcat, 2, start)
	case two == "<=":
		l.op(tokLe, 2, start)
	case two == ">=":
		l.op(tokGe, 2, start)
	case two == "<>":
		l.op(tokNe, 2, start)
	case two == "!=":
		l.op(tokNe, 2, start)
	case two == "@>":
		l.op(tokContains, 2, start)
	case two == "<@":
		l.op(tokContained, 2, start)
	case two == "&&":
		l.op(tokOverlap, 2, start)
	default:
		l.scanSingle(c, start)
	}
}

func (l *lexer) scanSingle(c byte, start int) {
	switch c {
	case ',':
		l.op(tokComma, 1, start)
	case '(':
		l.op(tokLParen, 1, start)
	case ')':
		l.op(tokRParen, 1, start)
	case '.':
		l.op(tokDot, 1, start)
	case ';':
		l.op(tokSemicolon, 1, start)
	case '*':
		l.op(tokStar, 1, start)
	case '=':
		l.op(tokEq, 1, start)
	case '<':
		l.op(tokLt, 1, start)
	case '>':
		l.op(tokGt, 1, start)
	case '+':
		l.op(tokPlus, 1, start)
	case '-':
		l.op(tokMinus, 1, start)
	case '/':
		l.op(tokSlash, 1, start)
	case '%':
		l.op(tokPercent, 1, start)
	default:
		l.fail(start, "unexpected character", string(c))
	}
}

func (l *lexer) op(kind tokenKind, width, start int) {
	l.emit(kind, l.src[start:start+width], start)
	l.pos += width
}

// --- character class helpers ---

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) || c == '$' }

// isBoundary reports whether a rune ends an unquoted word legitimately (whitespace or
// punctuation), so a trailing multi-byte rune that is not a boundary is the error.
func isBoundary(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(",()[].;*=<>+-/%:'\"|@&!", r)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}
