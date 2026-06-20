package format

import "encoding/binary"

// PageType identifies the structure of a content page (spec 03 §5.1). Page 1
// (the header page) has no page-type byte; every other page begins with one.
type PageType byte

const (
	PageFree           PageType = 0x00 // freelist leaf page (spec 03 §11)
	PageFreelistTrunk  PageType = 0x01 // freelist trunk page
	PageCatalog        PageType = 0x02 // catalog/schema page (spec 03 §9)
	PageBTreeInterior  PageType = 0x03 // secondary-index B-tree interior node
	PageBTreeLeaf      PageType = 0x04 // secondary-index B-tree leaf node
	PageVectorSegment  PageType = 0x05 // fixed-stride columnar vector segment (§7)
	PageVectorOverflow PageType = 0x06 // overflow page for wide vectors (§7.6)
	PageHNSWGraph      PageType = 0x07 // HNSW node records + neighbor lists (§10.1)
	PageIVFCentroid    PageType = 0x08 // IVF centroid table page (§10.2)
	PageIVFPosting     PageType = 0x09 // IVF posting (inverted) list page (§10.2)
	PagePQCodebook     PageType = 0x0A // PQ/OPQ codebook page (§10.3)
	PageDiskANNGraph   PageType = 0x0B // DiskANN fixed-degree graph block (§10.4)
	PageMetaColumn     PageType = 0x0C // metadata column-store page (§8)
	PageMetaOverflow   PageType = 0x0D // overflow for variable-length metadata (§8.4)
	PagePtrMap         PageType = 0x0E // pointer-map page for vacuum/relocation (§12)
	PageBlob           PageType = 0x0F // large-object blob page (§12)
)

// String renders a PageType for diagnostics and the verifier.
func (t PageType) String() string {
	switch t {
	case PageFree:
		return "free"
	case PageFreelistTrunk:
		return "freelist_trunk"
	case PageCatalog:
		return "catalog"
	case PageBTreeInterior:
		return "btree_interior"
	case PageBTreeLeaf:
		return "btree_leaf"
	case PageVectorSegment:
		return "vector_segment"
	case PageVectorOverflow:
		return "vector_overflow"
	case PageHNSWGraph:
		return "hnsw_graph"
	case PageIVFCentroid:
		return "ivf_centroid"
	case PageIVFPosting:
		return "ivf_posting"
	case PagePQCodebook:
		return "pq_codebook"
	case PageDiskANNGraph:
		return "diskann_graph"
	case PageMetaColumn:
		return "meta_column"
	case PageMetaOverflow:
		return "meta_overflow"
	case PagePtrMap:
		return "ptrmap"
	case PageBlob:
		return "blob"
	default:
		return "page?"
	}
}

// Common page header layout (spec 03 §6.1). The header is a 32-byte prefix at
// the start of every content page. The page checksum is NOT stored in this
// prefix: spec 03 §6.1 resolves its own ambiguity by stating the checksum
// "physically lives" in the last 4 bytes of the page and covers bytes
// [0, page_size-4). This package follows that physical placement: the 32-byte
// prefix carries fields at offsets 0..27, offsets 28..31 are reserved zero, and
// the checksum is written/verified at the page tail via WritePageChecksum and
// VerifyPageChecksum.
const (
	PageHeaderSize   = 32 // bytes 0..31 at page start
	PageChecksumSize = 4  // trailing checksum at page end
)

// Common page header flag bits (spec 03 §6.1, field at offset 1).
const (
	PageFlagSlotted      = 1 << 0 // slotted (variable-length) layout
	PageFlagOverflowCont = 1 << 1 // overflow continuation page
	PageFlagCompressed   = 1 << 2 // compressed body
)

// PageHeader is the decoded 32-byte common page header (spec 03 §6.1).
type PageHeader struct {
	Type       PageType // offset 0
	Flags      byte     // offset 1
	FreeOffset uint16   // offset 2 (slotted) / occupied-slot count (fixed-stride)
	CellCount  uint16   // offset 4
	FragBytes  uint16   // offset 6 (slotted only)
	PageLSN    uint64   // offset 8 (ARIES page-LSN, spec 05)
	SectionID  uint32   // offset 16
	NextPage   PageNo   // offset 20
	PrevPage   PageNo   // offset 24
	// offsets 28..31 reserved zero
}

// Encode writes the 32-byte header into the first PageHeaderSize bytes of page.
func (h *PageHeader) Encode(page []byte) {
	_ = page[PageHeaderSize-1] // bounds-check hint
	page[0] = byte(h.Type)
	page[1] = h.Flags
	binary.LittleEndian.PutUint16(page[2:], h.FreeOffset)
	binary.LittleEndian.PutUint16(page[4:], h.CellCount)
	binary.LittleEndian.PutUint16(page[6:], h.FragBytes)
	binary.LittleEndian.PutUint64(page[8:], h.PageLSN)
	binary.LittleEndian.PutUint32(page[16:], h.SectionID)
	binary.LittleEndian.PutUint32(page[20:], uint32(h.NextPage))
	binary.LittleEndian.PutUint32(page[24:], uint32(h.PrevPage))
	page[28] = 0
	page[29] = 0
	page[30] = 0
	page[31] = 0
}

// DecodePageHeader parses the 32-byte common page header from the start of page.
func DecodePageHeader(page []byte) (PageHeader, error) {
	if len(page) < PageHeaderSize {
		return PageHeader{}, ErrShortBuffer
	}
	return PageHeader{
		Type:       PageType(page[0]),
		Flags:      page[1],
		FreeOffset: binary.LittleEndian.Uint16(page[2:]),
		CellCount:  binary.LittleEndian.Uint16(page[4:]),
		FragBytes:  binary.LittleEndian.Uint16(page[6:]),
		PageLSN:    binary.LittleEndian.Uint64(page[8:]),
		SectionID:  binary.LittleEndian.Uint32(page[16:]),
		NextPage:   PageNo(binary.LittleEndian.Uint32(page[20:])),
		PrevPage:   PageNo(binary.LittleEndian.Uint32(page[24:])),
	}, nil
}

// WritePageChecksum computes CRC32C over page[0:len-4] and stores it in the last
// 4 bytes (spec 03 §6.2). It is a no-op (writes zero) when algo is ChecksumNone.
// The caller passes the full page slice of exactly page_size bytes.
func WritePageChecksum(page []byte, algo ChecksumAlgo) {
	n := len(page)
	if algo == ChecksumNone {
		binary.LittleEndian.PutUint32(page[n-4:], 0)
		return
	}
	sum := CRC32C(page[:n-4])
	binary.LittleEndian.PutUint32(page[n-4:], sum)
}

// VerifyPageChecksum recomputes the tail checksum and compares it to the stored
// value (spec 03 §6.2). It returns ErrCorrupt on mismatch and nil when checksums
// are disabled.
func VerifyPageChecksum(page []byte, algo ChecksumAlgo) error {
	if algo == ChecksumNone {
		return nil
	}
	n := len(page)
	stored := binary.LittleEndian.Uint32(page[n-4:])
	if CRC32C(page[:n-4]) != stored {
		return ErrCorrupt
	}
	return nil
}

// BodyRange returns the [start, end) byte range of the usable body within a
// content page, after the common header and before the per-page reserved tail
// and checksum (spec 03 §6.1). pageSize and reservedPerPage come from the header.
func BodyRange(pageSize int, reservedPerPage byte) (start, end int) {
	return PageHeaderSize, pageSize - int(reservedPerPage) - PageChecksumSize
}
