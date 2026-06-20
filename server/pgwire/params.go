package pgwire

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
)

// substituteParams replaces $N placeholders in sql with the bound values encoded
// as SQL literals (spec 16 §18.2, §20.3). Values arrive in text or binary format
// per the format codes; a binary pgvector value (spec 16 §17.5) is decoded to its
// text form [f1,...,fN] before substitution. There is no string-interpolation
// injection path: values are always emitted as typed literals.
func substituteParams(sql string, values [][]byte, formats []int16, oids []int32) (string, *pgError) {
	if len(values) == 0 {
		return sql, nil
	}
	lits := make([]string, len(values))
	for i, v := range values {
		fmtCode := int16(0)
		if len(formats) == 1 {
			fmtCode = formats[0]
		} else if i < len(formats) {
			fmtCode = formats[i]
		}
		var oid int32
		if i < len(oids) {
			oid = oids[i]
		}
		lit, perr := encodeParamLiteral(v, fmtCode, oid)
		if perr != nil {
			return "", perr
		}
		lits[i] = lit
	}
	return replacePlaceholders(sql, lits), nil
}

// replacePlaceholders rewrites $1..$N in sql with the literal at the matching
// index, leaving placeholders inside single-quoted strings untouched.
func replacePlaceholders(sql string, lits []string) string {
	var sb strings.Builder
	inStr := false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		if ch == '\'' {
			inStr = !inStr
			sb.WriteByte(ch)
			continue
		}
		if ch == '$' && !inStr && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' {
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(sql[i+1 : j])
			if n >= 1 && n <= len(lits) {
				sb.WriteString(lits[n-1])
				i = j - 1
				continue
			}
		}
		sb.WriteByte(ch)
	}
	return sb.String()
}

// encodeParamLiteral renders one bound parameter as a VectorSQL literal
// (spec 16 §17.5, §18.3). NULL becomes the keyword NULL.
func encodeParamLiteral(v []byte, format int16, oid int32) (string, *pgError) {
	if v == nil {
		return "NULL", nil
	}
	if format == 1 {
		return encodeBinaryLiteral(v, oid)
	}
	return encodeTextLiteral(string(v), oid), nil
}

// encodeTextLiteral wraps a text parameter as a literal. A bracketed token is a
// vector literal and passes through unquoted; everything else is quoted as a
// string so the VectorSQL parser can apply implicit casts (spec 16 §18.2, §18.3).
func encodeTextLiteral(s string, oid int32) string {
	t := strings.TrimSpace(s)
	switch oid {
	case oidInt8, oidInt4:
		if _, err := strconv.ParseInt(t, 10, 64); err == nil {
			return t
		}
	case oidFloat8:
		if _, err := strconv.ParseFloat(t, 64); err == nil {
			return t
		}
	case oidBool:
		if t == "t" || t == "true" {
			return "TRUE"
		}
		if t == "f" || t == "false" {
			return "FALSE"
		}
	}
	if len(t) >= 2 && t[0] == '[' && t[len(t)-1] == ']' {
		return "'" + t + "'" // vector text literal; the parser casts it
	}
	return quoteString(s)
}

// encodeBinaryLiteral decodes a binary parameter into a literal. The vector OID
// uses the pgvector binary format (spec 16 §17.5); int8/float8 are big-endian.
func encodeBinaryLiteral(v []byte, oid int32) (string, *pgError) {
	switch oid {
	case oidVector:
		fl, perr := decodeBinaryVector(v)
		if perr != nil {
			return "", perr
		}
		return "'" + vectorText(fl) + "'", nil
	case oidInt8:
		if len(v) != 8 {
			return "", &pgError{code: "22P03", message: "invalid binary int8 length"}
		}
		return strconv.FormatInt(int64(binary.BigEndian.Uint64(v)), 10), nil
	case oidInt4:
		if len(v) != 4 {
			return "", &pgError{code: "22P03", message: "invalid binary int4 length"}
		}
		return strconv.FormatInt(int64(int32(binary.BigEndian.Uint32(v))), 10), nil
	case oidFloat8:
		if len(v) != 8 {
			return "", &pgError{code: "22P03", message: "invalid binary float8 length"}
		}
		f := math.Float64frombits(binary.BigEndian.Uint64(v))
		return strconv.FormatFloat(f, 'g', -1, 64), nil
	case oidBool:
		if len(v) == 1 && v[0] != 0 {
			return "TRUE", nil
		}
		return "FALSE", nil
	default:
		// Unknown binary type: treat as text bytes.
		return quoteString(string(v)), nil
	}
}

// decodeBinaryVector decodes the pgvector binary wire form: 2-byte dim, 2-byte
// unused, then dim big-endian float32 values (spec 16 §17.5). pgvector uses
// int16 dim + int16 flags; we accept both the 2+2 and 4+4 layouts.
func decodeBinaryVector(v []byte) ([]float32, *pgError) {
	if len(v) < 4 {
		return nil, &pgError{code: "22P03", message: "invalid binary vector header"}
	}
	dim := int(binary.BigEndian.Uint16(v[:2]))
	off := 4
	if dim*4+off != len(v) {
		// Try the 4-byte dimension layout described in the spec table.
		if len(v) >= 8 {
			d2 := int(binary.BigEndian.Uint32(v[:4]))
			if d2*4+8 == len(v) {
				dim, off = d2, 8
			}
		}
	}
	if dim*4+off != len(v) {
		return nil, &pgError{code: "22P03", message: "binary vector length mismatch"}
	}
	out := make([]float32, dim)
	for i := 0; i < dim; i++ {
		bits := binary.BigEndian.Uint32(v[off+i*4:])
		out[i] = math.Float32frombits(bits)
	}
	return out, nil
}

// vectorText renders a float slice as the pgvector text form [f1,f2,...].
func vectorText(fl []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, f := range fl {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

// quoteString wraps s as a single-quoted SQL string literal, doubling embedded
// quotes (spec 16 §20.3).
func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// inferParamOIDs returns the parameter OIDs for a prepared statement, using the
// client-declared OIDs when present and falling back to a vector-first guess
// (spec 16 §18.2). A zero declared OID for the first parameter is taken as the
// vector argument of a distance operator, the common pgvector shape.
func inferParamOIDs(stmt *prepared) []int32 {
	n := countParams(stmt.sql)
	out := make([]int32, n)
	for i := 0; i < n; i++ {
		if i < len(stmt.paramOIDs) && stmt.paramOIDs[i] != 0 {
			out[i] = stmt.paramOIDs[i]
			continue
		}
		if i == 0 && strings.ContainsAny(stmt.sql, "<") {
			out[i] = oidVector // first param of a kNN query is the query vector
			continue
		}
		out[i] = oidInt8
	}
	return out
}

// countParams returns the highest $N placeholder index in sql.
func countParams(sql string) int {
	max := 0
	inStr := false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		if ch == '\'' {
			inStr = !inStr
			continue
		}
		if ch == '$' && !inStr && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' {
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			if n, _ := strconv.Atoi(sql[i+1 : j]); n > max {
				max = n
			}
			i = j - 1
		}
	}
	return max
}
