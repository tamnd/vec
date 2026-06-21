package pgwire

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// newTestWriter returns a msgWriter backed by a byte buffer.
func newTestWriter() (*msgWriter, *bytes.Buffer) {
	var buf bytes.Buffer
	return &msgWriter{w: bufio.NewWriter(&buf)}, &buf
}

// TestReadyForQueryGolden checks the exact bytes of ReadyForQuery against the
// documented format: 'Z' 00 00 00 05 'I' (spec 16 §17.7).
func TestReadyForQueryGolden(t *testing.T) {
	w, buf := newTestWriter()
	if err := w.writeReadyForQuery('I'); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.flush()
	want := []byte{'Z', 0x00, 0x00, 0x00, 0x05, 'I'}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("ReadyForQuery bytes = % x, want % x", buf.Bytes(), want)
	}
}

// TestAuthenticationOkGolden checks 'R' 00 00 00 08 00 00 00 00 (spec 16 §17.2).
func TestAuthenticationOkGolden(t *testing.T) {
	w, buf := newTestWriter()
	_ = w.writeAuthenticationOk()
	_ = w.flush()
	want := []byte{'R', 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("AuthenticationOk bytes = % x, want % x", buf.Bytes(), want)
	}
}

// TestErrorResponseRoundTrip writes an ErrorResponse and decodes its fields.
func TestErrorResponseRoundTrip(t *testing.T) {
	w, buf := newTestWriter()
	_ = w.writeErrorResponse(pgError{code: "0A000", message: "feature not supported", hint: "see docs"})
	_ = w.flush()

	out := buf.Bytes()
	if out[0] != msgErrorResponse {
		t.Fatalf("tag = %q, want E", out[0])
	}
	n := int(binary.BigEndian.Uint32(out[1:5]))
	if n != len(out)-1 {
		t.Fatalf("length field = %d, want %d", n, len(out)-1)
	}
	body := out[5:]
	if errorField(body, 'C') != "0A000" {
		t.Fatalf("sqlstate = %q", errorField(body, 'C'))
	}
	if errorField(body, 'M') != "feature not supported" {
		t.Fatalf("message = %q", errorField(body, 'M'))
	}
	if errorField(body, 'H') != "see docs" {
		t.Fatalf("hint = %q", errorField(body, 'H'))
	}
}

// TestDataRowRoundTrip encodes a DataRow with a NULL and reads it back.
func TestDataRowRoundTrip(t *testing.T) {
	w, buf := newTestWriter()
	_ = w.writeDataRow([][]byte{[]byte("42"), nil, []byte("hi")})
	_ = w.flush()
	out := buf.Bytes()
	if out[0] != msgDataRow {
		t.Fatalf("tag = %q", out[0])
	}
	got := dataRowStrings(out[5:])
	if len(got) != 3 || got[0] != "42" || got[1] != "" || got[2] != "hi" {
		t.Fatalf("decoded row = %v", got)
	}
}

// TestRowDescriptionRoundTrip encodes and re-decodes a RowDescription.
func TestRowDescriptionRoundTrip(t *testing.T) {
	w, buf := newTestWriter()
	fields := []fieldDesc{
		{name: "id", colNum: 1, typeOID: oidInt8, typeSize: 8, typeMod: -1},
		{name: "dist", colNum: 2, typeOID: oidFloat8, typeSize: 8, typeMod: -1},
	}
	_ = w.writeRowDescription(fields)
	_ = w.flush()
	body := buf.Bytes()[5:]
	br := newBodyReader(body)
	n, _ := br.int16()
	if n != 2 {
		t.Fatalf("field count = %d, want 2", n)
	}
	name, _ := br.cstring()
	if name != "id" {
		t.Fatalf("first field = %q, want id", name)
	}
	for i := 0; i < 5; i++ { // skip tableOID..formatCode of field 1
		switch i {
		case 0, 2:
			_, _ = br.int32()
		case 1, 3, 4:
		}
	}
}

// TestSubstituteParams checks parameter substitution into SQL.
func TestSubstituteParams(t *testing.T) {
	sql := "SELECT id FROM docs ORDER BY emb <-> $1 LIMIT $2"
	vals := [][]byte{[]byte("[1,2,3]"), []byte("5")}
	oids := []int32{oidVector, oidInt8}
	got, perr := substituteParams(sql, vals, []int16{0, 0}, oids)
	if perr != nil {
		t.Fatalf("substitute: %v", perr)
	}
	want := "SELECT id FROM docs ORDER BY emb <-> '[1,2,3]' LIMIT 5"
	if got != want {
		t.Fatalf("substituted = %q, want %q", got, want)
	}
}

// TestDecodeBinaryVector checks the pgvector binary decode (spec 16 §17.5).
func TestDecodeBinaryVector(t *testing.T) {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 2) // dim
	b = binary.BigEndian.AppendUint16(b, 0) // unused
	b = binary.BigEndian.AppendUint32(b, math.Float32bits(1.5))
	b = binary.BigEndian.AppendUint32(b, math.Float32bits(-2.0))
	fl, perr := decodeBinaryVector(b)
	if perr != nil {
		t.Fatalf("decode: %v", perr)
	}
	if len(fl) != 2 || fl[0] != 1.5 || fl[1] != -2.0 {
		t.Fatalf("decoded vector = %v, want [1.5 -2]", fl)
	}
}
