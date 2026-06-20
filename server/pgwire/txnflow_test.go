package pgwire

import (
	"strings"
	"testing"
)

func TestTransactionFlow(t *testing.T) {
	db := testDB(t)
	c := dial(t, Options{DB: db, AuthMode: "none"})
	c.startup("vec")
	c.expectReady()

	run := func(sql string) []backendMsg {
		c.query(sql)
		msgs := c.readUntil(msgReadyForQuery)
		for _, m := range msgs {
			if m.tag == msgErrorResponse {
				t.Fatalf("%q: %s", sql, decodeError(m.body))
			}
		}
		return msgs
	}
	run("CREATE TABLE docs (id bigint PRIMARY KEY, emb vector(3), title text)")
	// state goes T after BEGIN
	c.query("BEGIN")
	msgs := c.readUntil(msgReadyForQuery)
	if msgs[len(msgs)-1].body[0] != 'T' {
		t.Fatalf("state after BEGIN = %q want T", msgs[len(msgs)-1].body[0])
	}
	run("INSERT INTO docs (id, emb, title) VALUES (7, '[1,0,0]', 'seven')")
	c.query("COMMIT")
	msgs = c.readUntil(msgReadyForQuery)
	if msgs[len(msgs)-1].body[0] != 'I' {
		t.Fatalf("state after COMMIT = %q want I", msgs[len(msgs)-1].body[0])
	}
	// the committed row is visible
	c.query("SELECT id, title FROM docs ORDER BY emb <-> '[1,0,0]' LIMIT 1")
	rows := [][]string{}
	for _, m := range c.readUntil(msgReadyForQuery) {
		if m.tag == msgDataRow {
			rows = append(rows, dataRowStrings(m.body))
		}
	}
	if len(rows) != 1 || rows[0][0] != "7" || !strings.Contains(rows[0][1], "seven") {
		t.Fatalf("post-commit row = %v", rows)
	}
}
