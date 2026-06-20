// Package vectorsql implements the VectorSQL language surface of spec 12: a
// hand-written recursive-descent lexer and parser over the formal grammar (§15),
// the AST the parser emits (§17.2), the strict error model (§14), and a binder
// that resolves a parsed statement against the catalog and lowers a kNN SELECT to
// the planner's BoundQuery. VectorSQL is a strict SQL subset: no JOIN, no CTE, one
// FROM table per query, with first-class vector distance operators and a FUSION
// clause for hybrid search.
package vectorsql

import "fmt"

// VecError is every error the VectorSQL frontend raises (spec 12 §14.4). It carries
// a stable symbolic code, a numeric code for programmatic dispatch, a human message,
// an optional detail string, and, for parse errors, the byte offset in the source.
type VecError struct {
	Code    string // symbolic code, e.g. "E_PARSE"
	Numeric int    // numeric code, e.g. 1000
	Message string // human-readable description
	Detail  string // optional context (column name, expected dimension, spec reference)
	Offset  int    // byte offset within the statement text; -1 when not positional
}

// Error renders the symbolic code and message, plus the offset for parse errors.
func (e *VecError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// The error taxonomy of spec 12 §14.4. Each pairs a symbolic code with its numeric
// code; the constructors below stamp both onto a VecError.
const (
	codeParse             = "E_PARSE"
	codeUnsupported       = "E_UNSUPPORTED"
	codeIdent             = "E_IDENT"
	codeType              = "E_TYPE"
	codeDim               = "E_DIM"
	codeParam             = "E_PARAM"
	codeParamTypeConflict = "E_PARAM_TYPE_CONFLICT"
	codeNullVec           = "E_NULL_VEC"
	codeVectorGroup       = "E_VECTOR_GROUP"
	codeVectorDistinct    = "E_VECTOR_DISTINCT"
	codeAggKNNConflict    = "E_AGG_KNN_CONFLICT"
	codePlan              = "E_PLAN"
)

var numericOf = map[string]int{
	codeParse:             1000,
	codeUnsupported:       1001,
	codeIdent:             1002,
	codeType:              1003,
	codeDim:               1004,
	codeParam:             1005,
	codeParamTypeConflict: 1006,
	codeNullVec:           1007,
	codeVectorGroup:       1020,
	codeVectorDistinct:    1021,
	codeAggKNNConflict:    1022,
	codePlan:              1060,
}

// newError builds a VecError with the numeric code looked up from the symbolic code.
func newError(code, message, detail string, offset int) *VecError {
	return &VecError{Code: code, Numeric: numericOf[code], Message: message, Detail: detail, Offset: offset}
}

// parseError reports a syntax error at a byte offset (spec 12 §14.5).
func parseError(offset int, message, detail string) *VecError {
	return newError(codeParse, fmt.Sprintf("syntax error at position %d: %s", offset, message), detail, offset)
}

// unsupportedError reports a construct outside VectorSQL's scope, always with a spec
// reference so the user knows the rejection is intentional (spec 12 §14.5).
func unsupportedError(message, detail string) *VecError {
	return newError(codeUnsupported, message, detail, -1)
}

// identError reports an unresolved table, column, or index name (spec 12 §14.6).
func identError(message, detail string) *VecError {
	return newError(codeIdent, message, detail, -1)
}

// typeError reports a type mismatch in an expression (spec 12 §14.6).
func typeError(message, detail string) *VecError {
	return newError(codeType, message, detail, -1)
}

// dimError reports a vector dimension mismatch (spec 12 §14.6).
func dimError(message, detail string) *VecError {
	return newError(codeDim, message, detail, -1)
}
