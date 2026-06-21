package bulk

import (
	"bytes"
	"errors"
	"testing"
)

func sampleFrames() []Frame {
	return []Frame{
		{PageNo: 1, Version: 10, CommitTS: 1700000000000000000, Data: []byte("page-one")},
		{PageNo: 2, Version: 11, CommitTS: 1700000000500000000, Data: []byte{}},
		{PageNo: 7, Version: 12, CommitTS: 1700000001000000000, Data: bytes.Repeat([]byte{0xAB}, 4096)},
	}
}

func TestSegmentRoundTrip(t *testing.T) {
	frames := sampleFrames()
	raw, err := EncodeSegment(10, 12, frames)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	seg, err := DecodeSegment(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if seg.BaseVersion != 10 || seg.EndVersion != 12 {
		t.Fatalf("versions: got base=%d end=%d", seg.BaseVersion, seg.EndVersion)
	}
	if len(seg.Frames) != len(frames) {
		t.Fatalf("frame count: got %d want %d", len(seg.Frames), len(frames))
	}
	for i, f := range seg.Frames {
		w := frames[i]
		if f.PageNo != w.PageNo || f.Version != w.Version || f.CommitTS != w.CommitTS {
			t.Errorf("frame %d header mismatch: %+v vs %+v", i, f, w)
		}
		if !bytes.Equal(f.Data, w.Data) {
			t.Errorf("frame %d data mismatch", i)
		}
	}
}

func TestSegmentEmpty(t *testing.T) {
	raw, err := EncodeSegment(5, 5, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	seg, err := DecodeSegment(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(seg.Frames) != 0 {
		t.Fatalf("expected no frames, got %d", len(seg.Frames))
	}
}

func TestSegmentTornTail(t *testing.T) {
	raw, err := EncodeSegment(10, 12, sampleFrames())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Drop the last 4 bytes to simulate a torn write.
	torn := raw[:len(raw)-4]
	if _, err := DecodeSegment(torn); !errors.Is(err, ErrBadSegment) {
		t.Fatalf("expected ErrBadSegment for torn tail, got %v", err)
	}
}

func TestSegmentCorruptBody(t *testing.T) {
	raw, err := EncodeSegment(10, 12, sampleFrames())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	corrupt := append([]byte(nil), raw...)
	// Flip a byte inside the body (past the 32-byte header).
	corrupt[40] ^= 0xFF
	if _, err := DecodeSegment(corrupt); !errors.Is(err, ErrBadSegment) {
		t.Fatalf("expected ErrBadSegment for corrupt body, got %v", err)
	}
}

func TestSegmentCorruptHeader(t *testing.T) {
	raw, err := EncodeSegment(10, 12, sampleFrames())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	corrupt := append([]byte(nil), raw...)
	// Flip the base version; the header CRC must catch it.
	corrupt[8] ^= 0xFF
	if _, err := DecodeSegment(corrupt); !errors.Is(err, ErrBadSegment) {
		t.Fatalf("expected ErrBadSegment for corrupt header, got %v", err)
	}
}

func TestSegmentBadMagic(t *testing.T) {
	raw, err := EncodeSegment(1, 1, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	raw[0] = 'X'
	if _, err := DecodeSegment(raw); !errors.Is(err, ErrBadSegment) {
		t.Fatalf("expected ErrBadSegment for bad magic, got %v", err)
	}
}

func TestSegmentCompressRoundTrip(t *testing.T) {
	frames := sampleFrames()
	raw, err := EncodeSegment(10, 12, frames)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	gz, err := CompressSegment(raw)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	// gzip-wrapped path
	seg, err := DecodeSegmentReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("decode gz: %v", err)
	}
	if len(seg.Frames) != len(frames) {
		t.Fatalf("gz frame count: got %d want %d", len(seg.Frames), len(frames))
	}
	// plain path through the same reader entry point
	seg2, err := DecodeSegmentReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode plain: %v", err)
	}
	if len(seg2.Frames) != len(frames) {
		t.Fatalf("plain frame count: got %d want %d", len(seg2.Frames), len(frames))
	}
}
