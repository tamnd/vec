package pgwire

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	vec "github.com/tamnd/vec"
)

// testDB opens an ephemeral vec database for the wire tests.
func testDB(t *testing.T) *vec.DB {
	t.Helper()
	db, err := vec.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// client drives a test connection: it writes frontend messages and reads
// backend frames over a net.Pipe.
type client struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// dial wires a Conn serve loop to an in-process client over net.Pipe.
func dial(t *testing.T, opts Options) *client {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	c := newConn(srvConn, opts)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.serve(ctx)
	return &client{t: t, conn: cliConn, r: bufio.NewReader(cliConn)}
}

// startup sends a v3 StartupMessage with the given user.
func (c *client) startup(user string) {
	c.t.Helper()
	var b []byte
	b = binary.BigEndian.AppendUint32(b, protoVersion3)
	b = append(b, "user"...)
	b = append(b, 0)
	b = append(b, user...)
	b = append(b, 0)
	b = append(b, 0) // terminating null
	var framed []byte
	framed = binary.BigEndian.AppendUint32(framed, uint32(len(b)+4))
	framed = append(framed, b...)
	if _, err := c.conn.Write(framed); err != nil {
		c.t.Fatalf("startup write: %v", err)
	}
}

// sendPassword sends a PasswordMessage.
func (c *client) sendPassword(pw string) {
	var body []byte
	body = append(body, pw...)
	body = append(body, 0)
	c.writeFrame(fePassword, body)
}

// writeFrame writes a tagged frontend message.
func (c *client) writeFrame(tag byte, body []byte) {
	c.t.Helper()
	var hdr [5]byte
	hdr[0] = tag
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(body)+4))
	if _, err := c.conn.Write(append(hdr[:], body...)); err != nil {
		c.t.Fatalf("write frame %q: %v", tag, err)
	}
}

// query sends a simple Query.
func (c *client) query(sql string) {
	var body []byte
	body = append(body, sql...)
	body = append(body, 0)
	c.writeFrame(feQuery, body)
}

// backendMsg is one decoded backend frame.
type backendMsg struct {
	tag  byte
	body []byte
}

// read reads one backend frame.
func (c *client) read() backendMsg {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	tag, err := c.r.ReadByte()
	if err != nil {
		c.t.Fatalf("read tag: %v", err)
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(c.r, lenBuf[:]); err != nil {
		c.t.Fatalf("read len: %v", err)
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	body := make([]byte, n-4)
	if _, err := io.ReadFull(c.r, body); err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return backendMsg{tag: tag, body: body}
}

// readUntil reads frames until it sees a tag, returning all frames read.
func (c *client) readUntil(tag byte) []backendMsg {
	c.t.Helper()
	var msgs []backendMsg
	for i := 0; i < 200; i++ {
		m := c.read()
		msgs = append(msgs, m)
		if m.tag == tag {
			return msgs
		}
	}
	c.t.Fatalf("did not see tag %q", tag)
	return nil
}

// expectReady drains through the handshake to the first ReadyForQuery.
func (c *client) expectReady() {
	c.t.Helper()
	msgs := c.readUntil(msgReadyForQuery)
	last := msgs[len(msgs)-1]
	if last.body[0] != 'I' {
		c.t.Fatalf("ReadyForQuery state = %q, want I", last.body[0])
	}
}

// dataRowStrings decodes a DataRow body into text column values.
func dataRowStrings(body []byte) []string {
	br := newBodyReader(body)
	n, _ := br.int16()
	out := make([]string, 0, n)
	for i := 0; i < int(n); i++ {
		l, _ := br.int32()
		if l < 0 {
			out = append(out, "")
			continue
		}
		v, _ := br.bytesN(int(l))
		out = append(out, string(v))
	}
	return out
}

func TestHandshakeTrust(t *testing.T) {
	db := testDB(t)
	c := dial(t, Options{DB: db, Version: "test", AuthMode: "trust"})
	c.startup("alice")
	c.expectReady()
}

func TestSelectVersion(t *testing.T) {
	db := testDB(t)
	c := dial(t, Options{DB: db, Version: "9.9.9", AuthMode: "none"})
	c.startup("vec")
	c.expectReady()

	c.query("SELECT version()")
	msgs := c.readUntil(msgReadyForQuery)
	var got string
	for _, m := range msgs {
		if m.tag == msgDataRow {
			got = dataRowStrings(m.body)[0]
		}
	}
	if !strings.Contains(got, "vec 9.9.9") {
		t.Fatalf("version() = %q, want it to mention vec 9.9.9", got)
	}
}

func TestPasswordAuth(t *testing.T) {
	db := testDB(t)
	verified := ""
	c := dial(t, Options{
		DB:       db,
		AuthMode: "password",
		Verify: func(user, pw string) error {
			verified = user + ":" + pw
			return nil
		},
	})
	c.startup("bob")
	// Server should request a cleartext password first.
	m := c.read()
	if m.tag != msgAuthentication {
		t.Fatalf("expected Authentication, got %q", m.tag)
	}
	c.sendPassword("s3cret")
	c.expectReady()
	if verified != "bob:s3cret" {
		t.Fatalf("verify saw %q, want bob:s3cret", verified)
	}
}

func TestCreateInsertKNN(t *testing.T) {
	db := testDB(t)
	c := dial(t, Options{DB: db, AuthMode: "none"})
	c.startup("vec")
	c.expectReady()

	exec := func(sql, wantTagPrefix string) {
		t.Helper()
		c.query(sql)
		msgs := c.readUntil(msgReadyForQuery)
		for _, m := range msgs {
			if m.tag == msgErrorResponse {
				t.Fatalf("query %q error: %s", sql, decodeError(m.body))
			}
		}
		var tag string
		for _, m := range msgs {
			if m.tag == msgCommandComplete {
				tag = string(m.body[:len(m.body)-1])
			}
		}
		if !strings.HasPrefix(tag, wantTagPrefix) {
			t.Fatalf("query %q tag = %q, want prefix %q", sql, tag, wantTagPrefix)
		}
	}

	exec("CREATE TABLE docs (id bigint PRIMARY KEY, emb vector(3), title text)", "CREATE TABLE")
	exec("INSERT INTO docs (id, emb, title) VALUES (1, '[1,0,0]', 'one')", "INSERT 0 1")
	exec("INSERT INTO docs (id, emb, title) VALUES (2, '[0,1,0]', 'two')", "INSERT 0 1")
	exec("INSERT INTO docs (id, emb, title) VALUES (3, '[0,0,1]', 'three')", "INSERT 0 1")

	// kNN query: nearest to [0.9,0.1,0] is id 1.
	c.query("SELECT id, title, emb <-> '[0.9,0.1,0]' AS dist FROM docs ORDER BY emb <-> '[0.9,0.1,0]' LIMIT 2")
	msgs := c.readUntil(msgReadyForQuery)
	var rows [][]string
	for _, m := range msgs {
		switch m.tag {
		case msgErrorResponse:
			t.Fatalf("kNN error: %s", decodeError(m.body))
		case msgDataRow:
			rows = append(rows, dataRowStrings(m.body))
		}
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0][0] != "1" || rows[0][1] != "one" {
		t.Fatalf("nearest row = %v, want id 1 title one", rows[0])
	}
}

func TestUnsupportedStatement(t *testing.T) {
	db := testDB(t)
	c := dial(t, Options{DB: db, AuthMode: "none"})
	c.startup("vec")
	c.expectReady()

	// A JOIN is outside the supported subset; the parser rejects it or we do.
	c.query("SELECT a.id FROM docs a JOIN tags b ON a.id = b.id")
	msgs := c.readUntil(msgReadyForQuery)
	var code string
	for _, m := range msgs {
		if m.tag == msgErrorResponse {
			code = errorField(m.body, 'C')
		}
	}
	if code != "0A000" && code != "42601" && code != "42P01" {
		t.Fatalf("unsupported JOIN code = %q, want a rejection sqlstate", code)
	}
}

func TestExtendedProtocol(t *testing.T) {
	db := testDB(t)
	c := dial(t, Options{DB: db, AuthMode: "none"})
	c.startup("vec")
	c.expectReady()

	// Seed via simple query.
	for _, sql := range []string{
		"CREATE TABLE docs (id bigint PRIMARY KEY, emb vector(3), title text)",
		"INSERT INTO docs (id, emb, title) VALUES (1, '[1,0,0]', 'one')",
		"INSERT INTO docs (id, emb, title) VALUES (2, '[0,1,0]', 'two')",
	} {
		c.query(sql)
		for _, m := range c.readUntil(msgReadyForQuery) {
			if m.tag == msgErrorResponse {
				t.Fatalf("seed %q: %s", sql, decodeError(m.body))
			}
		}
	}

	// Parse / Bind / Execute / Sync for a parameterized kNN select.
	parseBody := buildParse("q1", "SELECT id, title FROM docs ORDER BY emb <-> $1 LIMIT 1", nil)
	c.writeFrame(feParse, parseBody)

	bindBody := buildBind("p1", "q1", []bindParam{{text: "[0,0.9,0]"}}, nil)
	c.writeFrame(feBind, bindBody)

	execBody := buildExecute("p1", 0)
	c.writeFrame(feExecute, execBody)
	c.writeFrame(feSync, nil)

	msgs := c.readUntil(msgReadyForQuery)
	var sawParse, sawBind bool
	var rows [][]string
	for _, m := range msgs {
		switch m.tag {
		case msgParseComplete:
			sawParse = true
		case msgBindComplete:
			sawBind = true
		case msgErrorResponse:
			t.Fatalf("extended error: %s", decodeError(m.body))
		case msgDataRow:
			rows = append(rows, dataRowStrings(m.body))
		}
	}
	if !sawParse || !sawBind {
		t.Fatalf("missing ParseComplete/BindComplete: parse=%v bind=%v", sawParse, sawBind)
	}
	if len(rows) != 1 || rows[0][0] != "2" {
		t.Fatalf("extended kNN rows = %v, want id 2 nearest", rows)
	}
}

// --- extended-protocol body builders ---

func buildParse(name, sql string, oids []int32) []byte {
	var b []byte
	b = append(b, name...)
	b = append(b, 0)
	b = append(b, sql...)
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, uint16(len(oids)))
	for _, o := range oids {
		b = binary.BigEndian.AppendUint32(b, uint32(o))
	}
	return b
}

type bindParam struct {
	text   string
	binary []byte
}

func buildBind(portal, stmt string, params []bindParam, resultFmts []int16) []byte {
	var b []byte
	b = append(b, portal...)
	b = append(b, 0)
	b = append(b, stmt...)
	b = append(b, 0)
	// Parameter formats: one per param.
	b = binary.BigEndian.AppendUint16(b, uint16(len(params)))
	for _, p := range params {
		if p.binary != nil {
			b = binary.BigEndian.AppendUint16(b, 1)
		} else {
			b = binary.BigEndian.AppendUint16(b, 0)
		}
	}
	// Parameter values.
	b = binary.BigEndian.AppendUint16(b, uint16(len(params)))
	for _, p := range params {
		if p.binary != nil {
			b = binary.BigEndian.AppendUint32(b, uint32(len(p.binary)))
			b = append(b, p.binary...)
		} else {
			b = binary.BigEndian.AppendUint32(b, uint32(len(p.text)))
			b = append(b, p.text...)
		}
	}
	// Result formats.
	b = binary.BigEndian.AppendUint16(b, uint16(len(resultFmts)))
	for _, f := range resultFmts {
		b = binary.BigEndian.AppendUint16(b, uint16(f))
	}
	return b
}

func buildExecute(portal string, maxRows int32) []byte {
	var b []byte
	b = append(b, portal...)
	b = append(b, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(maxRows))
	return b
}

// --- error field decoding ---

func decodeError(body []byte) string {
	return errorField(body, 'M')
}

func errorField(body []byte, field byte) string {
	i := 0
	for i < len(body) && body[i] != 0 {
		ft := body[i]
		i++
		start := i
		for i < len(body) && body[i] != 0 {
			i++
		}
		val := string(body[start:i])
		i++ // skip null
		if ft == field {
			return val
		}
	}
	return ""
}
