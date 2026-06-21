package pgwire

import (
	"strings"
)

// handleCatalog intercepts the catalog/introspection and SET statements that
// drivers send before real work (spec 16 §4.6, §18.1). It returns handled=true
// when it answered the statement; perr is non-nil only on an interceptor error.
//
// raw is the original-cased SQL, low is its lowercased form. resultFormats is
// passed through so single-column answers honor a binary result request.
func (c *Conn) handleCatalog(raw, low string, resultFormats []int16) (bool, *pgError) {
	// SET [LOCAL|SESSION] name = value (spec 16 §18.1 step 2).
	if strings.HasPrefix(low, "set ") {
		c.applySet(raw)
		_ = c.w.writeCommandComplete("SET")
		return true, nil
	}

	// CREATE EXTENSION / DROP EXTENSION are accepted as no-ops (spec 16 §21.2).
	if strings.HasPrefix(low, "create extension") {
		_ = c.w.writeCommandComplete("CREATE EXTENSION")
		return true, nil
	}
	if strings.HasPrefix(low, "drop extension") {
		_ = c.w.writeCommandComplete("DROP EXTENSION")
		return true, nil
	}

	// discard / reset are accepted as no-ops.
	if strings.HasPrefix(low, "discard") {
		_ = c.w.writeCommandComplete("DISCARD ALL")
		return true, nil
	}
	if strings.HasPrefix(low, "reset ") {
		_ = c.w.writeCommandComplete("RESET")
		return true, nil
	}

	resp, ok := c.matchCatalog(raw, low)
	if !ok {
		return false, nil
	}
	c.writeScalarResult(resp, resultFormats)
	return true, nil
}

// catalogResult is a small synthetic result set returned by the interceptor.
type catalogResult struct {
	columns []fieldDesc
	rows    [][][]byte
	tag     string // CommandComplete tag, e.g. "SELECT 1"
}

// matchCatalog matches the recognized catalog patterns (spec 16 §4.6). The match
// is a tolerant substring/prefix test, not a full parse.
func (c *Conn) matchCatalog(raw, low string) (catalogResult, bool) {
	switch {
	case contains(low, "version()"):
		return scalarText("version", "PostgreSQL 15.0 (vec "+c.opts.Version+") on x86_64"), true

	case contains(low, "current_schema()") || contains(low, "current_schema"):
		return scalarText("current_schema", "public"), true

	case contains(low, "current_database()"):
		return scalarText("current_database", c.databaseName()), true

	case contains(low, "current_user") || contains(low, "session_user") || contains(low, "user()"):
		return scalarText("current_user", c.user), true

	case contains(low, "set_config"):
		return scalarText("set_config", ""), true

	case contains(low, "extversion") && contains(low, "vector"):
		// Langchain checks the pgvector extension version (spec 16 §21.2).
		return scalarText("extversion", "0.8.0"), true

	case strings.HasPrefix(low, "show "):
		return c.handleShow(low), true

	case contains(low, "from pg_namespace"):
		return scalarText("nspname", "public"), true

	case contains(low, "from pg_type"):
		return pgTypeCatalog(), true

	case contains(low, "select 1") && !contains(low, "from"):
		return scalarInt("?column?", 1), true
	}
	return catalogResult{}, false
}

// handleShow answers SHOW name (spec 16 §4.6).
func (c *Conn) handleShow(low string) catalogResult {
	name := strings.TrimSpace(strings.TrimPrefix(low, "show "))
	name = strings.TrimSuffix(name, ";")
	switch name {
	case "server_version":
		return scalarText("server_version", "15.0")
	case "server_encoding", "client_encoding":
		return scalarText(name, "UTF8")
	case "transaction_isolation":
		return scalarText("transaction_isolation", "read committed")
	case "standard_conforming_strings":
		return scalarText("standard_conforming_strings", "on")
	case "search_path":
		return scalarText("search_path", "public")
	default:
		if v, ok := c.session[name]; ok {
			return scalarText(name, v)
		}
		return scalarText(name, "")
	}
}

// applySet records a SET name = value override on the session (spec 16 §4.6).
func (c *Conn) applySet(raw string) {
	body := strings.TrimSpace(raw[len("set "):])
	low := strings.ToLower(body)
	if strings.HasPrefix(low, "local ") {
		body = strings.TrimSpace(body[len("local "):])
	} else if strings.HasPrefix(low, "session ") {
		body = strings.TrimSpace(body[len("session "):])
	}
	name, value, ok := strings.Cut(body, "=")
	if !ok {
		// SET name TO value form.
		fields := strings.Fields(body)
		if len(fields) >= 3 && strings.EqualFold(fields[1], "to") {
			name, value = fields[0], strings.Join(fields[2:], " ")
		} else {
			return
		}
	}
	name = strings.ToLower(strings.TrimSpace(name))
	value = strings.Trim(strings.TrimSpace(value), "'\";")
	c.session[name] = value
}

// databaseName returns the requested database, defaulting to the engine path stem.
func (c *Conn) databaseName() string {
	if c.database != "" {
		return c.database
	}
	p := c.opts.DB.Path()
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	return strings.TrimSuffix(p, ".vec")
}

// writeScalarResult writes a catalog result: RowDescription, DataRows, then
// CommandComplete.
func (c *Conn) writeScalarResult(r catalogResult, resultFormats []int16) {
	applyResultFormats(r.columns, resultFormats)
	_ = c.w.writeRowDescription(r.columns)
	for _, row := range r.rows {
		_ = c.w.writeDataRow(row)
	}
	tag := r.tag
	if tag == "" {
		tag = "SELECT " + itoa(len(r.rows))
	}
	_ = c.w.writeCommandComplete(tag)
}

// applyResultFormats stamps the requested format code onto every column. A
// single format code applies to all columns (spec 16 §17.4).
func applyResultFormats(cols []fieldDesc, formats []int16) {
	if len(formats) == 0 {
		return
	}
	for i := range cols {
		if len(formats) == 1 {
			cols[i].formatCode = formats[0]
		} else if i < len(formats) {
			cols[i].formatCode = formats[i]
		}
	}
}

// --- synthetic result constructors ---

func scalarText(name, value string) catalogResult {
	return catalogResult{
		columns: []fieldDesc{{name: name, colNum: 1, typeOID: oidText, typeSize: -1, typeMod: -1}},
		rows:    [][][]byte{{[]byte(value)}},
	}
}

func scalarInt(name string, v int64) catalogResult {
	return catalogResult{
		columns: []fieldDesc{{name: name, colNum: 1, typeOID: oidInt8, typeSize: 8, typeMod: -1}},
		rows:    [][][]byte{{[]byte(itoa64(v))}},
	}
}

// pgTypeCatalog returns a small synthetic pg_type result including the reserved
// vector OID 3999 (spec 16 §4.6, §17.4).
func pgTypeCatalog() catalogResult {
	cols := []fieldDesc{
		{name: "oid", colNum: 1, typeOID: oidInt8, typeSize: 8, typeMod: -1},
		{name: "typname", colNum: 2, typeOID: oidText, typeSize: -1, typeMod: -1},
	}
	types := []struct {
		oid  int64
		name string
	}{
		{oidInt8, "int8"},
		{oidInt4, "int4"},
		{oidText, "text"},
		{oidBool, "bool"},
		{oidFloat8, "float8"},
		{oidBytea, "bytea"},
		{oidVarchar, "varchar"},
		{oidVector, "vector"},
	}
	var rows [][][]byte
	for _, t := range types {
		rows = append(rows, [][]byte{[]byte(itoa64(t.oid)), []byte(t.name)})
	}
	return catalogResult{columns: cols, rows: rows}
}

// contains is a case-insensitive substring helper used by the interceptor.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
