package vectorsql

// tokenKind enumerates the lexical token classes the scanner produces (spec 12 §2).
type tokenKind uint8

const (
	tokEOF tokenKind = iota
	tokIdent
	tokKeyword
	tokInt
	tokFloat
	tokString // single-quoted string literal (may be a vector/sparse/multivec literal)
	tokParam  // :name or $1
	// punctuation and operators
	tokComma
	tokLParen
	tokRParen
	tokDot
	tokSemicolon
	tokStar
	// comparison
	tokEq
	tokNe
	tokLt
	tokLe
	tokGt
	tokGe
	// arithmetic
	tokPlus
	tokMinus
	tokSlash
	tokPercent
	tokConcat // ||
	// vector distance
	tokL2   // <->
	tokCos  // <=>
	tokIP   // <#>
	tokL1   // <+>
	tokCast // ::
	// json access
	tokArrow  // ->
	tokArrow2 // ->>
	// array containment
	tokContains  // @>
	tokContained // <@
	tokOverlap   // &&
)

// Token is one lexical unit with its source byte offset for error reporting.
type Token struct {
	Kind   tokenKind
	Text   string // the canonical text: lowercased keyword/ident, raw literal value
	Offset int    // byte offset of the token start in the source
	// Param fields, set when Kind == tokParam.
	ParamName string // for :name
	ParamPos  int    // for $N, 1-based; 0 for named params
}

// keywords maps a lowercased word to its keyword text. A word in this set lexes as a
// tokKeyword; everything else is a tokIdent (spec 12 §2.3). Keywords are
// case-insensitive; an identifier that needs to use one of these words must be
// double-quoted.
var keywords = map[string]struct{}{
	"add": {}, "against": {}, "all": {}, "alter": {}, "analyze": {}, "and": {},
	"as": {}, "asc": {}, "begin": {}, "between": {}, "by": {}, "case": {},
	"check": {}, "column": {}, "commit": {}, "conflict": {}, "copy": {},
	"create": {}, "cross": {}, "deallocate": {}, "default": {}, "delete": {},
	"desc": {}, "distinct": {}, "do": {}, "drop": {}, "else": {}, "end": {},
	"execute": {}, "exists": {}, "explain": {}, "filter": {}, "first": {},
	"format": {}, "from": {}, "fusion": {}, "group": {}, "having": {}, "if": {},
	"ilike": {}, "in": {}, "index": {}, "insert": {}, "into": {}, "is": {},
	"isolation": {}, "join": {}, "keyword": {}, "key": {}, "last": {}, "level": {},
	"like": {}, "limit": {}, "local": {}, "match": {}, "not": {}, "null": {},
	"nulls": {}, "offset": {}, "on": {}, "or": {}, "order": {}, "pragma": {},
	"prepare": {}, "primary": {}, "profile": {}, "release": {}, "rename": {},
	"returning": {}, "rollback": {}, "savepoint": {}, "select": {}, "session": {},
	"set": {}, "table": {}, "then": {}, "to": {}, "transaction": {}, "unique": {},
	"update": {}, "upsert": {}, "using": {}, "values": {}, "when": {}, "where": {},
	"with": {}, "true": {}, "false": {},
}

// isKeyword reports whether the lowercased word is a reserved keyword.
func isKeyword(lower string) bool {
	_, ok := keywords[lower]
	return ok
}
