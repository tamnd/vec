// Package pgwire implements the PostgreSQL v3 frontend/backend wire protocol
// over net.Conn so pgvector clients (psql, psycopg, pgx, asyncpg, JDBC) talk to
// the vec engine without code changes (spec 16 §4, §17, §18).
//
// The package is pure standard library. It hand-rolls the framed message format
// (spec 16 §17): a 1-byte type tag plus a 4-byte big-endian length that includes
// itself, except the untagged StartupMessage. The supported SQL subset is the
// pgvector kNN surface (spec 16 §4.5, §4.7); everything outside it returns an
// ErrorResponse with SQLSTATE 0A000.
package pgwire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Backend message type tags (server to client), spec 16 §17.
const (
	msgAuthentication  = 'R'
	msgParameterStatus = 'S'
	msgBackendKeyData  = 'K'
	msgReadyForQuery   = 'Z'
	msgRowDescription  = 'T'
	msgDataRow         = 'D'
	msgCommandComplete = 'C'
	msgErrorResponse   = 'E'
	msgEmptyQuery      = 'I'
	msgParseComplete   = '1'
	msgBindComplete    = '2'
	msgCloseComplete   = '3'
	msgNoData          = 'n'
	msgParamDescr      = 't'
)

// Frontend message type tags (client to server), spec 16 §17.
const (
	feQuery     = 'Q'
	feParse     = 'P'
	feBind      = 'B'
	feDescribe  = 'D'
	feExecute   = 'E'
	feSync      = 'S'
	feClose     = 'C'
	feTerminate = 'X'
	fePassword  = 'p'
	feFlush     = 'H'
	feCopyData  = 'd'
	feCopyDone  = 'c'
	feCopyFail  = 'f'
)

// Protocol magic numbers in the startup message (spec 16 §17.1). The spec names
// the cancel magic 0x04D2162F; the canonical libpq values are CancelRequest
// 0x04D2162E, SSLRequest 0x04D2162F, GSSENCRequest 0x04D21630. Both are handled.
const (
	protoVersion3 = 0x00030000
	cancelMagic   = 0x04D2162E
	sslMagic      = 0x04D2162F
	gssMagic      = 0x04D21630
)

// PostgreSQL type OIDs used in RowDescription (spec 16 §4.4).
const (
	oidInt8    = 20
	oidText    = 25
	oidBool    = 16
	oidBytea   = 17
	oidFloat8  = 701
	oidInt4    = 23
	oidVarchar = 1043
	// oidVector is the reserved fake vector OID (spec 16 §4.4, §17.4).
	oidVector = 3999
)

// errShortRead reports a truncated message body.
var errShortRead = errors.New("pgwire: short read")

// msgReader frames inbound messages off a buffered reader.
type msgReader struct {
	r *bufio.Reader
}

// frame is one inbound message: a type tag and its body (length stripped).
type frame struct {
	tag  byte
	body []byte
}

// readStartup reads the untagged startup or cancel/ssl request message. It has
// no type tag: a 4-byte length followed by length-4 body bytes (spec 16 §17.1).
func (m *msgReader) readStartup() ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(m.r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	if n < 4 {
		return nil, fmt.Errorf("pgwire: bad startup length %d", n)
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(m.r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// readFrame reads one tagged message (spec 16 §17). The length field counts
// itself, so the body is length-4 bytes.
func (m *msgReader) readFrame() (frame, error) {
	tag, err := m.r.ReadByte()
	if err != nil {
		return frame{}, err
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(m.r, lenBuf[:]); err != nil {
		return frame{}, err
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	if n < 4 {
		return frame{}, fmt.Errorf("pgwire: bad message length %d for tag %q", n, tag)
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(m.r, body); err != nil {
		return frame{}, err
	}
	return frame{tag: tag, body: body}, nil
}

// --- inbound body decoding ---

// bodyReader walks a message body, tracking position.
type bodyReader struct {
	b   []byte
	pos int
}

func newBodyReader(b []byte) *bodyReader { return &bodyReader{b: b} }

func (b *bodyReader) remaining() int { return len(b.b) - b.pos }

func (b *bodyReader) int16() (int16, error) {
	if b.remaining() < 2 {
		return 0, errShortRead
	}
	v := int16(binary.BigEndian.Uint16(b.b[b.pos:]))
	b.pos += 2
	return v, nil
}

func (b *bodyReader) int32() (int32, error) {
	if b.remaining() < 4 {
		return 0, errShortRead
	}
	v := int32(binary.BigEndian.Uint32(b.b[b.pos:]))
	b.pos += 4
	return v, nil
}

func (b *bodyReader) byteVal() (byte, error) {
	if b.remaining() < 1 {
		return 0, errShortRead
	}
	v := b.b[b.pos]
	b.pos++
	return v, nil
}

// cstring reads a null-terminated string.
func (b *bodyReader) cstring() (string, error) {
	for i := b.pos; i < len(b.b); i++ {
		if b.b[i] == 0 {
			s := string(b.b[b.pos:i])
			b.pos = i + 1
			return s, nil
		}
	}
	return "", errShortRead
}

// bytesN reads exactly n bytes.
func (b *bodyReader) bytesN(n int) ([]byte, error) {
	if n < 0 {
		return nil, nil // NULL marker
	}
	if b.remaining() < n {
		return nil, errShortRead
	}
	out := b.b[b.pos : b.pos+n]
	b.pos += n
	return out, nil
}

// --- outbound message building ---

// msgWriter builds and flushes outbound messages over a buffered writer.
type msgWriter struct {
	w *bufio.Writer
}

// writeMsg frames body under tag (1-byte tag + 4-byte length + body) and writes
// it (spec 16 §17). The length field counts itself plus the body.
func (m *msgWriter) writeMsg(tag byte, body []byte) error {
	var hdr [5]byte
	hdr[0] = tag
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(body)+4))
	if _, err := m.w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := m.w.Write(body)
	return err
}

func (m *msgWriter) flush() error { return m.w.Flush() }

// builder accumulates a message body with the typed put helpers.
type builder struct{ buf []byte }

func (b *builder) int16(v int16) {
	b.buf = binary.BigEndian.AppendUint16(b.buf, uint16(v))
}

func (b *builder) int32(v int32) {
	b.buf = binary.BigEndian.AppendUint32(b.buf, uint32(v))
}

func (b *builder) byteVal(v byte) { b.buf = append(b.buf, v) }

// cstring appends a string with its terminating null.
func (b *builder) cstring(s string) {
	b.buf = append(b.buf, s...)
	b.buf = append(b.buf, 0)
}

func (b *builder) bytes(p []byte) { b.buf = append(b.buf, p...) }

// --- backend message writers (spec 16 §17.2 through §17.6) ---

// writeAuthenticationOk sends 'R' with authType 0.
func (m *msgWriter) writeAuthenticationOk() error {
	var b builder
	b.int32(0)
	return m.writeMsg(msgAuthentication, b.buf)
}

// writeAuthenticationCleartext sends 'R' with authType 3 (spec 16 §17.2).
func (m *msgWriter) writeAuthenticationCleartext() error {
	var b builder
	b.int32(3)
	return m.writeMsg(msgAuthentication, b.buf)
}

// writeAuthenticationMD5 sends 'R' authType 5 with a 4-byte salt (spec 16 §17.2).
func (m *msgWriter) writeAuthenticationMD5(salt [4]byte) error {
	var b builder
	b.int32(5)
	b.bytes(salt[:])
	return m.writeMsg(msgAuthentication, b.buf)
}

// writeParameterStatus sends 'S' name/value (spec 16 §17.3).
func (m *msgWriter) writeParameterStatus(name, value string) error {
	var b builder
	b.cstring(name)
	b.cstring(value)
	return m.writeMsg(msgParameterStatus, b.buf)
}

// writeBackendKeyData sends 'K' pid + cancel key (spec 16 §4.3).
func (m *msgWriter) writeBackendKeyData(pid, key int32) error {
	var b builder
	b.int32(pid)
	b.int32(key)
	return m.writeMsg(msgBackendKeyData, b.buf)
}

// writeReadyForQuery sends 'Z' with the transaction state byte 'I'/'T'/'E'
// (spec 16 §17.7).
func (m *msgWriter) writeReadyForQuery(state byte) error {
	return m.writeMsg(msgReadyForQuery, []byte{state})
}

// fieldDesc is one column descriptor in a RowDescription (spec 16 §17.4).
type fieldDesc struct {
	name       string
	tableOID   int32
	colNum     int16
	typeOID    int32
	typeSize   int16
	typeMod    int32
	formatCode int16
}

// writeRowDescription sends 'T' with one descriptor per column (spec 16 §17.4).
func (m *msgWriter) writeRowDescription(fields []fieldDesc) error {
	var b builder
	b.int16(int16(len(fields)))
	for _, f := range fields {
		b.cstring(f.name)
		b.int32(f.tableOID)
		b.int16(f.colNum)
		b.int32(f.typeOID)
		b.int16(f.typeSize)
		b.int32(f.typeMod)
		b.int16(f.formatCode)
	}
	return m.writeMsg(msgRowDescription, b.buf)
}

// writeDataRow sends 'D' with one value per column; a nil value encodes NULL via
// a -1 length field (spec 16 §17.5).
func (m *msgWriter) writeDataRow(values [][]byte) error {
	var b builder
	b.int16(int16(len(values)))
	for _, v := range values {
		if v == nil {
			b.int32(-1)
			continue
		}
		b.int32(int32(len(v)))
		b.bytes(v)
	}
	return m.writeMsg(msgDataRow, b.buf)
}

// writeCommandComplete sends 'C' with the command tag (e.g. "SELECT 5").
func (m *msgWriter) writeCommandComplete(tag string) error {
	var b builder
	b.cstring(tag)
	return m.writeMsg(msgCommandComplete, b.buf)
}

// writeEmptyQueryResponse sends 'I' for an empty query string.
func (m *msgWriter) writeEmptyQueryResponse() error {
	return m.writeMsg(msgEmptyQuery, nil)
}

func (m *msgWriter) writeParseComplete() error { return m.writeMsg(msgParseComplete, nil) }
func (m *msgWriter) writeBindComplete() error  { return m.writeMsg(msgBindComplete, nil) }
func (m *msgWriter) writeCloseComplete() error { return m.writeMsg(msgCloseComplete, nil) }
func (m *msgWriter) writeNoData() error        { return m.writeMsg(msgNoData, nil) }

// writeParameterDescription sends 't' with the parameter type OIDs (spec 16 §18.2).
func (m *msgWriter) writeParameterDescription(oids []int32) error {
	var b builder
	b.int16(int16(len(oids)))
	for _, o := range oids {
		b.int32(o)
	}
	return m.writeMsg(msgParamDescr, b.buf)
}

// pgError carries the fields of an ErrorResponse (spec 16 §17.6).
type pgError struct {
	severity string
	code     string // SQLSTATE
	message  string
	hint     string
	detail   string
}

func (e pgError) Error() string { return e.code + ": " + e.message }

// writeErrorResponse sends 'E' with the severity, SQLSTATE, message, and the
// optional detail/hint fields, terminated by a null byte (spec 16 §17.6).
func (m *msgWriter) writeErrorResponse(e pgError) error {
	sev := e.severity
	if sev == "" {
		sev = "ERROR"
	}
	var b builder
	b.byteVal('S')
	b.cstring(sev)
	b.byteVal('V')
	b.cstring(sev)
	b.byteVal('C')
	b.cstring(e.code)
	b.byteVal('M')
	b.cstring(e.message)
	if e.detail != "" {
		b.byteVal('D')
		b.cstring(e.detail)
	}
	if e.hint != "" {
		b.byteVal('H')
		b.cstring(e.hint)
	}
	b.byteVal(0)
	return m.writeMsg(msgErrorResponse, b.buf)
}
