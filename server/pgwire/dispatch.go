package pgwire

import (
	"context"
	"fmt"
	"strings"
)

// sprintf is a thin wrapper so message builders avoid importing fmt directly.
func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// handleSimpleQuery runs the simple-query protocol ('Q'): parse the SQL string,
// dispatch it, and finish with ReadyForQuery (spec 16 §4.4). An ErrorResponse in
// this path is always followed by ReadyForQuery (spec 16 §4.7).
func (c *Conn) handleSimpleQuery(ctx context.Context, body []byte) error {
	br := newBodyReader(body)
	sql, err := br.cstring()
	if err != nil {
		return err
	}
	sql = strings.TrimSpace(sql)
	if sql == "" {
		if err := c.w.writeEmptyQueryResponse(); err != nil {
			return err
		}
		return c.ready()
	}

	// A simple-query string may hold several statements separated by ';'. Run
	// each in turn; stop at the first error (spec 16 §4.4).
	for _, stmt := range splitStatements(sql) {
		if stmt == "" {
			continue
		}
		if perr := c.runStatement(ctx, stmt, nil, nil); perr != nil {
			if err := c.w.writeErrorResponse(*perr); err != nil {
				return err
			}
			if c.state == txnInBlock {
				c.state = txnFailed
			}
			break
		}
	}
	return c.ready()
}

// splitStatements splits a simple-query string on top-level semicolons, leaving
// semicolons inside single-quoted string literals intact.
func splitStatements(sql string) []string {
	var out []string
	var sb strings.Builder
	inStr := false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		switch {
		case ch == '\'':
			inStr = !inStr
			sb.WriteByte(ch)
		case ch == ';' && !inStr:
			out = append(out, strings.TrimSpace(sb.String()))
			sb.Reset()
		default:
			sb.WriteByte(ch)
		}
	}
	if s := strings.TrimSpace(sb.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// runStatement is the shared dispatch used by both the simple and extended
// paths (spec 16 §18.1). It runs the catalog interceptor, the SET interceptor,
// transaction control, then the VectorSQL bridge. It writes RowDescription and
// DataRow messages directly and returns a non-nil *pgError on failure so the
// caller decides how to frame it.
//
// resultFormats, when non-nil, requests binary/text per output column for the
// extended path; nil means all text (simple query, spec 16 §4.4).
func (c *Conn) runStatement(ctx context.Context, sql string, resultFormats []int16, _ *prepared) *pgError {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	low := strings.ToLower(trimmed)

	// 1. Transaction control (spec 16 §18.1 step 3).
	if tag, handled, perr := c.handleTxnControl(low); handled {
		if perr != nil {
			return perr
		}
		_ = c.w.writeCommandComplete(tag)
		return nil
	}

	// 2. Catalog interceptor + SET (spec 16 §18.1 steps 1, 2).
	if handled, perr := c.handleCatalog(trimmed, low, resultFormats); handled {
		return perr
	}

	// 3. VectorSQL bridge (spec 16 §18.1 steps 4-7).
	return c.execVectorSQL(ctx, trimmed, resultFormats)
}

// handleTxnControl maps BEGIN/COMMIT/ROLLBACK to the connection transaction
// (spec 16 §4.6, §5.2). It returns the command tag and whether it handled the
// statement.
func (c *Conn) handleTxnControl(low string) (string, bool, *pgError) {
	switch low {
	case "begin", "begin transaction", "start transaction":
		if c.txn != nil {
			// Nested begin: PostgreSQL warns and keeps the open block.
			return "BEGIN", true, nil
		}
		txn, err := c.opts.DB.Begin(context.Background(), true)
		if err != nil {
			return "", true, c.mapError(err)
		}
		c.txn = txn
		c.state = txnInBlock
		return "BEGIN", true, nil
	case "commit", "commit transaction", "end":
		if c.txn != nil {
			if err := c.txn.Commit(); err != nil {
				c.txn = nil
				c.state = txnIdle
				return "", true, c.mapError(err)
			}
			c.txn = nil
		}
		c.state = txnIdle
		return "COMMIT", true, nil
	case "rollback", "rollback transaction", "abort":
		c.closeTxn()
		return "ROLLBACK", true, nil
	}
	return "", false, nil
}

// --- extended query protocol (spec 16 §17.7, §5.3) ---

// handleParse handles a Parse message: statement name, SQL, and parameter type
// OIDs (spec 16 §17.7 step 1). It stores the prepared statement and replies
// with ParseComplete.
func (c *Conn) handleParse(body []byte) error {
	br := newBodyReader(body)
	name, err := br.cstring()
	if err != nil {
		return err
	}
	sql, err := br.cstring()
	if err != nil {
		return err
	}
	nParams, err := br.int16()
	if err != nil {
		return err
	}
	oids := make([]int32, 0, nParams)
	for i := 0; i < int(nParams); i++ {
		oid, err := br.int32()
		if err != nil {
			return err
		}
		oids = append(oids, oid)
	}
	c.stmts[name] = &prepared{sql: strings.TrimSpace(sql), paramOIDs: oids}
	return c.w.writeParseComplete()
}

// handleBind handles a Bind message: statement name, portal name, parameter
// formats, parameter values, and result formats (spec 16 §17.7 step 2). It
// substitutes the parameter values into the SQL as literals and stores the
// portal.
func (c *Conn) handleBind(body []byte) error {
	br := newBodyReader(body)
	portalName, err := br.cstring()
	if err != nil {
		return err
	}
	stmtName, err := br.cstring()
	if err != nil {
		return err
	}
	stmt, ok := c.stmts[stmtName]
	if !ok {
		return c.extendedError(pgError{code: "26000", message: sprintf("prepared statement %q does not exist", stmtName)})
	}

	// Parameter format codes.
	nFmt, err := br.int16()
	if err != nil {
		return err
	}
	paramFmts := make([]int16, nFmt)
	for i := range paramFmts {
		if paramFmts[i], err = br.int16(); err != nil {
			return err
		}
	}

	// Parameter values.
	nVals, err := br.int16()
	if err != nil {
		return err
	}
	values := make([][]byte, nVals)
	for i := 0; i < int(nVals); i++ {
		l, err := br.int32()
		if err != nil {
			return err
		}
		if l < 0 {
			values[i] = nil
			continue
		}
		v, err := br.bytesN(int(l))
		if err != nil {
			return err
		}
		// Copy: the body buffer is reused by the reader.
		values[i] = append([]byte(nil), v...)
	}

	// Result format codes.
	nRes, err := br.int16()
	if err != nil {
		return err
	}
	resFmts := make([]int16, nRes)
	for i := range resFmts {
		if resFmts[i], err = br.int16(); err != nil {
			return err
		}
	}

	subSQL, perr := substituteParams(stmt.sql, values, paramFmts, stmt.paramOIDs)
	if perr != nil {
		return c.extendedError(*perr)
	}
	c.portals[portalName] = &portal{stmt: stmt, sql: subSQL, resultFormats: resFmts}
	return c.w.writeBindComplete()
}

// handleDescribe handles a Describe message for a statement ('S') or a portal
// ('P'), spec 16 §17.7, §18.2. For a statement we reply with ParameterDescription
// and NoData (we do not pre-resolve a RowDescription before execution).
func (c *Conn) handleDescribe(body []byte) error {
	br := newBodyReader(body)
	kind, err := br.byteVal()
	if err != nil {
		return err
	}
	name, err := br.cstring()
	if err != nil {
		return err
	}
	if kind == 'S' {
		stmt, ok := c.stmts[name]
		if !ok {
			return c.extendedError(pgError{code: "26000", message: sprintf("prepared statement %q does not exist", name)})
		}
		oids := inferParamOIDs(stmt)
		if err := c.w.writeParameterDescription(oids); err != nil {
			return err
		}
	}
	// We do not know the row shape until execution, so report NoData. Clients
	// that need column metadata receive RowDescription at Execute time.
	return c.w.writeNoData()
}

// handleExecute handles an Execute message: portal name and a max-row limit
// (spec 16 §17.7 step 3). It runs the bound SQL and streams results.
func (c *Conn) handleExecute(ctx context.Context, body []byte) error {
	br := newBodyReader(body)
	portalName, err := br.cstring()
	if err != nil {
		return err
	}
	if _, err := br.int32(); err != nil { // max rows: we always return all
		return err
	}
	p, ok := c.portals[portalName]
	if !ok {
		return c.extendedError(pgError{code: "34000", message: sprintf("portal %q does not exist", portalName)})
	}
	if perr := c.runStatement(ctx, p.sql, p.resultFormats, p.stmt); perr != nil {
		return c.extendedError(*perr)
	}
	return nil
}

// handleClose handles a Close message for a statement or portal (spec 16 §17.7
// step 5). It evicts the named entry and replies CloseComplete.
func (c *Conn) handleClose(body []byte) error {
	br := newBodyReader(body)
	kind, err := br.byteVal()
	if err != nil {
		return err
	}
	name, err := br.cstring()
	if err != nil {
		return err
	}
	switch kind {
	case 'S':
		delete(c.stmts, name)
	case 'P':
		delete(c.portals, name)
	}
	return c.w.writeCloseComplete()
}

// extendedError writes an ErrorResponse in the extended path and marks the
// transaction failed when in a block. The matching Sync produces ReadyForQuery
// (spec 16 §17.7).
func (c *Conn) extendedError(e pgError) error {
	if c.state == txnInBlock {
		c.state = txnFailed
	}
	return c.w.writeErrorResponse(e)
}
