package format

import "errors"

// DefaultPageSize is the page size for a freshly created database when the caller
// does not specify one. A vector database reads and writes large fixed-stride
// segments, so it favors a larger page than a row store; 16 KiB matches the
// worked example in spec 03 §3.7 and amortizes the per-page header over many
// vector slots.
const DefaultPageSize = 16384

// ErrBadPageSize is returned when a requested page size is not a power of two in
// the legal range (spec 03 §2.2).
var ErrBadPageSize = errors.New("vec: invalid page size (must be power of two, 4096..65536)")

// ValidPageSize reports whether ps is a legal page size: a power of two between
// 4096 and 65536 inclusive (spec 03 §2.2, §3.4).
func ValidPageSize(ps int) bool {
	if ps < 4096 || ps > 65536 {
		return false
	}
	return ps&(ps-1) == 0
}

// UsablePageSize is the number of bytes on a page available to a page body after
// the per-page reserved tail (spec 03 §6.1). It excludes the reserved region used
// for encryption auth tags but includes the common page header and tail checksum,
// matching the SQLite notion of "usable size".
func (h *Header) UsablePageSize() int {
	return h.PageSize - int(h.ReservedPerPage)
}

// TrunkPage is a decoded freelist trunk page (spec 03 §11): a forward link to the
// next trunk and a slice of free leaf page numbers it carries.
type TrunkPage struct {
	Next  uint32
	Leafs []uint32
}

// trunkBodyStart is where the leaf-number array begins within a trunk page: right
// after the 32-byte common page header.
const trunkBodyStart = PageHeaderSize

// TrunkCapacity returns how many 4-byte leaf page numbers fit in one trunk page
// of the given size and reserved tail (spec 03 §11). It accounts for the common
// header and the trailing checksum.
func TrunkCapacity(pageSize int, reservedPerPage byte) int {
	_, end := BodyRange(pageSize, reservedPerPage)
	body := end - trunkBodyStart
	if body < 0 {
		return 0
	}
	return body / 4
}

// EncodeTrunk lays out a freelist trunk page into the full page buffer: a common
// header with Type=PageFreelistTrunk and NextPage=tp.Next, CellCount = number of
// leaves, then the leaf page numbers as little-endian u32 starting at the body.
// The caller stamps the page checksum afterward with WritePageChecksum.
func EncodeTrunk(page []byte, tp TrunkPage) {
	for i := range page {
		page[i] = 0
	}
	h := PageHeader{
		Type:      PageFreelistTrunk,
		NextPage:  PageNo(tp.Next),
		CellCount: uint16(len(tp.Leafs)),
	}
	h.Encode(page)
	off := trunkBodyStart
	for _, leaf := range tp.Leafs {
		putU32(page[off:], leaf)
		off += 4
	}
}

// DecodeTrunk parses a freelist trunk page laid out by EncodeTrunk.
func DecodeTrunk(page []byte) TrunkPage {
	h, _ := DecodePageHeader(page)
	n := int(h.CellCount)
	leafs := make([]uint32, n)
	off := trunkBodyStart
	for i := 0; i < n; i++ {
		leafs[i] = getU32(page[off:])
		off += 4
	}
	return TrunkPage{Next: uint32(h.NextPage), Leafs: leafs}
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func getU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
