package bulk

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// The WAL segment format from spec 17 §4.3 (Appendix B). A segment is the unit of
// incremental and streaming backup: a header naming the version range, a body of
// WAL frames in commit order, and a trailer with the frame count and a body CRC.
//
//	Header (32 bytes):
//	  [0..3]   magic "vecW"
//	  [4..7]   format version (uint32 big-endian, currently 1)
//	  [8..15]  base version (uint64 big-endian)
//	  [16..23] end version (uint64 big-endian)
//	  [24..27] frame count (uint32 big-endian)
//	  [28..31] CRC32 of header bytes 0..27
//	Body: sequence of frames
//	Trailer (8 bytes):
//	  [0..3] body CRC32 (uint32 big-endian)
//	  [4..7] trailer magic 0x76656357 ("vecW")
//
// The spec sketches the frame count in the trailer; this codec carries it in the
// header (covered by the header CRC) and re-asserts the trailer magic, so a torn
// tail is caught by either the trailer length, the trailer magic, or the body CRC.
const (
	segHeaderSize   = 32
	segTrailerSize  = 8
	segFormatVer    = 1
	segTrailerMagic = 0x76656357
)

var (
	segMagic = [4]byte{'v', 'e', 'c', 'W'}

	// ErrBadSegment is returned when a segment fails magic, CRC, or length checks.
	ErrBadSegment = errors.New("bulk: malformed WAL segment")
)

// Frame is one WAL frame: a full page image at a committed version (spec 05 §3.2,
// referenced by spec 17 §4.3). CommitTS is the wall-clock commit time in Unix
// nanoseconds, used by point-in-time recovery to map a timestamp to a version.
type Frame struct {
	PageNo   uint32
	Version  uint64
	CommitTS int64
	Data     []byte
}

// frame on-disk layout: page_no u32, version u64, commit_ts i64, data_len u32,
// data bytes, frame_crc32 u32 (all big-endian). The CRC covers the fixed prefix
// and the data, so a truncated or corrupted frame is detected on read.
const frameFixedPrefix = 4 + 8 + 8 + 4

// Segment is a decoded WAL segment.
type Segment struct {
	BaseVersion uint64
	EndVersion  uint64
	Frames      []Frame
}

// EncodeSegment serializes base/end versions and frames into a segment byte slice.
// When the frames are empty the segment is still well-formed (an empty body).
func EncodeSegment(baseVersion, endVersion uint64, frames []Frame) ([]byte, error) {
	var body bytes.Buffer
	for i := range frames {
		if err := writeFrame(&body, frames[i]); err != nil {
			return nil, err
		}
	}
	bodyBytes := body.Bytes()

	out := make([]byte, segHeaderSize, segHeaderSize+len(bodyBytes)+segTrailerSize)
	copy(out[0:4], segMagic[:])
	binary.BigEndian.PutUint32(out[4:8], segFormatVer)
	binary.BigEndian.PutUint64(out[8:16], baseVersion)
	binary.BigEndian.PutUint64(out[16:24], endVersion)
	binary.BigEndian.PutUint32(out[24:28], uint32(len(frames)))
	binary.BigEndian.PutUint32(out[28:32], crc32.ChecksumIEEE(out[0:28]))

	out = append(out, bodyBytes...)

	var trailer [segTrailerSize]byte
	binary.BigEndian.PutUint32(trailer[0:4], crc32.ChecksumIEEE(bodyBytes))
	binary.BigEndian.PutUint32(trailer[4:8], segTrailerMagic)
	out = append(out, trailer[:]...)
	return out, nil
}

func writeFrame(w *bytes.Buffer, f Frame) error {
	var prefix [frameFixedPrefix]byte
	binary.BigEndian.PutUint32(prefix[0:4], f.PageNo)
	binary.BigEndian.PutUint64(prefix[4:12], f.Version)
	binary.BigEndian.PutUint64(prefix[12:20], uint64(f.CommitTS))
	binary.BigEndian.PutUint32(prefix[20:24], uint32(len(f.Data)))
	w.Write(prefix[:])
	w.Write(f.Data)

	h := crc32.NewIEEE()
	h.Write(prefix[:])
	h.Write(f.Data)
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], h.Sum32())
	w.Write(crc[:])
	return nil
}

// DecodeSegment parses and verifies a segment byte slice. It checks the header
// magic, header CRC, declared frame count, body CRC, and trailer magic. A short or
// torn segment returns ErrBadSegment.
func DecodeSegment(b []byte) (Segment, error) {
	var seg Segment
	if len(b) < segHeaderSize+segTrailerSize {
		return seg, fmt.Errorf("%w: too short (%d bytes)", ErrBadSegment, len(b))
	}
	if !bytes.Equal(b[0:4], segMagic[:]) {
		return seg, fmt.Errorf("%w: bad header magic", ErrBadSegment)
	}
	if v := binary.BigEndian.Uint32(b[4:8]); v != segFormatVer {
		return seg, fmt.Errorf("%w: unsupported format version %d", ErrBadSegment, v)
	}
	if got := crc32.ChecksumIEEE(b[0:28]); got != binary.BigEndian.Uint32(b[28:32]) {
		return seg, fmt.Errorf("%w: header CRC mismatch", ErrBadSegment)
	}
	seg.BaseVersion = binary.BigEndian.Uint64(b[8:16])
	seg.EndVersion = binary.BigEndian.Uint64(b[16:24])
	frameCount := binary.BigEndian.Uint32(b[24:28])

	body := b[segHeaderSize : len(b)-segTrailerSize]
	trailer := b[len(b)-segTrailerSize:]
	if got := crc32.ChecksumIEEE(body); got != binary.BigEndian.Uint32(trailer[0:4]) {
		return seg, fmt.Errorf("%w: body CRC mismatch (torn tail)", ErrBadSegment)
	}
	if binary.BigEndian.Uint32(trailer[4:8]) != segTrailerMagic {
		return seg, fmt.Errorf("%w: bad trailer magic", ErrBadSegment)
	}

	frames, err := decodeFrames(body, frameCount)
	if err != nil {
		return seg, err
	}
	seg.Frames = frames
	return seg, nil
}

func decodeFrames(body []byte, want uint32) ([]Frame, error) {
	frames := make([]Frame, 0, want)
	off := 0
	for off < len(body) {
		if off+frameFixedPrefix+4 > len(body) {
			return nil, fmt.Errorf("%w: truncated frame header", ErrBadSegment)
		}
		pageNo := binary.BigEndian.Uint32(body[off : off+4])
		version := binary.BigEndian.Uint64(body[off+4 : off+12])
		commitTS := int64(binary.BigEndian.Uint64(body[off+12 : off+20]))
		dataLen := binary.BigEndian.Uint32(body[off+20 : off+24])
		dataStart := off + frameFixedPrefix
		dataEnd := dataStart + int(dataLen)
		if dataEnd+4 > len(body) {
			return nil, fmt.Errorf("%w: truncated frame data", ErrBadSegment)
		}
		data := body[dataStart:dataEnd]
		gotCRC := binary.BigEndian.Uint32(body[dataEnd : dataEnd+4])
		h := crc32.NewIEEE()
		h.Write(body[off:dataStart])
		h.Write(data)
		if h.Sum32() != gotCRC {
			return nil, fmt.Errorf("%w: frame CRC mismatch at page %d version %d", ErrBadSegment, pageNo, version)
		}
		frames = append(frames, Frame{
			PageNo:   pageNo,
			Version:  version,
			CommitTS: commitTS,
			Data:     append([]byte(nil), data...),
		})
		off = dataEnd + 4
	}
	if uint32(len(frames)) != want {
		return nil, fmt.Errorf("%w: frame count %d does not match header %d", ErrBadSegment, len(frames), want)
	}
	return frames, nil
}

// CompressSegment gzip-wraps a raw segment. The spec uses zstd in object storage;
// a stdlib-only build uses gzip, which is self-describing via its own magic so the
// reader detects compression without a flag.
func CompressSegment(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeSegmentReader decodes a segment from r, transparently decompressing a
// gzip-wrapped segment. It reads the whole segment into memory, which is bounded by
// the segment size (default 16 MiB per spec 17 §5.2).
func DecodeSegmentReader(r io.Reader) (Segment, error) {
	br := bufio.NewReader(r)
	if gzipMagic(br) {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return Segment{}, err
		}
		defer func() { _ = zr.Close() }()
		raw, err := io.ReadAll(zr)
		if err != nil {
			return Segment{}, err
		}
		return DecodeSegment(raw)
	}
	raw, err := io.ReadAll(br)
	if err != nil {
		return Segment{}, err
	}
	return DecodeSegment(raw)
}
