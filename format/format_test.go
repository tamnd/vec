package format

import (
	"bytes"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	for _, ps := range []int{4096, 8192, 16384, 32768, 65536} {
		h := NewHeader(ps, ChecksumCRC32C)
		h.Flags = FlagWAL | FlagHNSW
		h.CatalogRoot = 7
		h.CollectionCount = 3
		h.TxnHighWater = 0xDEADBEEFCAFE
		buf := make([]byte, HeaderSize)
		h.Encode(buf)
		got, err := DecodeHeader(buf)
		if err != nil {
			t.Fatalf("page %d: decode: %v", ps, err)
		}
		if got.PageSize != ps {
			t.Errorf("page %d: PageSize=%d", ps, got.PageSize)
		}
		if got.CatalogRoot != 7 || got.CollectionCount != 3 {
			t.Errorf("page %d: catalog/coll mismatch: %+v", ps, got)
		}
		if got.TxnHighWater != 0xDEADBEEFCAFE {
			t.Errorf("page %d: txn high water mismatch: %x", ps, got.TxnHighWater)
		}
		if got.Flags != (FlagWAL | FlagHNSW) {
			t.Errorf("page %d: flags mismatch: %x", ps, got.Flags)
		}
	}
}

func TestHeaderRejectsBadMagic(t *testing.T) {
	buf := make([]byte, HeaderSize)
	NewHeader(4096, ChecksumCRC32C).Encode(buf)
	buf[3] = 'X'
	if _, err := DecodeHeader(buf); err != ErrNotVecFile {
		t.Fatalf("want ErrNotVecFile, got %v", err)
	}
}

func TestHeaderRejectsCorruption(t *testing.T) {
	buf := make([]byte, HeaderSize)
	NewHeader(8192, ChecksumCRC32C).Encode(buf)
	buf[60] ^= 0xFF // flip schema cookie, breaking the header checksum
	if _, err := DecodeHeader(buf); err != ErrCorruptHeader {
		t.Fatalf("want ErrCorruptHeader, got %v", err)
	}
}

func TestPageHeaderRoundTrip(t *testing.T) {
	page := make([]byte, 4096)
	h := PageHeader{
		Type:      PageVectorSegment,
		Flags:     0,
		CellCount: 42,
		PageLSN:   1234567,
		SectionID: 9,
		NextPage:  100,
		PrevPage:  98,
	}
	h.Encode(page)
	WritePageChecksum(page, ChecksumCRC32C)
	if err := VerifyPageChecksum(page, ChecksumCRC32C); err != nil {
		t.Fatalf("verify: %v", err)
	}
	got, err := DecodePageHeader(page)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != h {
		t.Fatalf("mismatch:\n got %+v\nwant %+v", got, h)
	}
	// Corrupt a body byte; checksum must fail.
	page[2000] ^= 1
	if err := VerifyPageChecksum(page, ChecksumCRC32C); err != ErrCorrupt {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestVarintRecord(t *testing.T) {
	var dst []byte
	dst = AppendUvarint(dst, 300)
	dst = AppendVarint(dst, -7)
	dst = AppendBytes(dst, []byte("hello"))
	dst = AppendBytes(dst, []byte("vec"))

	u, k := Uvarint(dst)
	if u != 300 {
		t.Fatalf("uvarint=%d", u)
	}
	dst = dst[k:]
	s, k := Varint(dst)
	if s != -7 {
		t.Fatalf("varint=%d", s)
	}
	dst = dst[k:]
	b1, rest, err := TakeBytes(dst)
	if err != nil || !bytes.Equal(b1, []byte("hello")) {
		t.Fatalf("b1=%q err=%v", b1, err)
	}
	b2, _, err := TakeBytes(rest)
	if err != nil || !bytes.Equal(b2, []byte("vec")) {
		t.Fatalf("b2=%q err=%v", b2, err)
	}
}

func TestPageSizeEncoding(t *testing.T) {
	if got := encodePageSize(65536); got != 1 {
		t.Fatalf("65536 code=%d", got)
	}
	if ps, err := decodePageSize(1); err != nil || ps != 65536 {
		t.Fatalf("decode 1: %d %v", ps, err)
	}
	if _, err := decodePageSize(5000); err == nil {
		t.Fatal("want error for non-power-of-two")
	}
}
