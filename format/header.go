package format

import "encoding/binary"

// PageNo is a 1-based page number. Page 0 is never used; page 1 holds the
// database header (spec 03 §2.3). Zero is the nil page pointer.
type PageNo uint32

// HeaderSize is the fixed size of the database header at the start of page 1
// (spec 03 §3.1). It is exactly 100 bytes, matching SQLite; the rest of page 1
// is reserved zero in format generation 1.
const HeaderSize = 100

// magicString is the canonical file signature: the 21-byte ASCII string
// "tamnd vector format 1" with no trailing NUL (spec 03 §3.3 hex dump). It sits
// at offset 0; bytes 21..31 are NUL, filling a 32-byte magic block. The trailing
// digit is the format generation; a new generation makes old code reject the
// file outright.
var magicString = []byte("tamnd vector format 1")

// MagicBlockSize is the size of the full magic block including NUL padding.
const MagicBlockSize = 32

// FormatLevel is the read/write feature level this build understands. A fresh
// file is stamped with read=write=1 (spec 03 §4.5).
const FormatLevel = 1

// WriterVersion is the informational writer build number recorded nowhere in the
// 100-byte header directly but exposed through the API; kept for parity with kv.
const WriterVersion = 1

// EncryptionKind selects the at-rest cipher recorded at header offset 73
// (spec 03 §3.2, spec 23).
type EncryptionKind byte

const (
	EncNone      EncryptionKind = 0
	EncAES256GCM EncryptionKind = 1
)

// ChecksumAlgo selects the page/WAL checksum recorded at header offset 75
// (spec 03 §3.2, §6.2).
type ChecksumAlgo byte

const (
	ChecksumNone   ChecksumAlgo = 0
	ChecksumCRC32C ChecksumAlgo = 1
	ChecksumXXH64  ChecksumAlgo = 2
)

// Header feature flag bits (offset 72, spec 03 §4.3).
const (
	FlagWAL         = 1 << 0
	FlagEncryption  = 1 << 1
	FlagCompression = 1 << 2
	FlagPQCodebook  = 1 << 3
	FlagDiskANN     = 1 << 4
	FlagHNSW        = 1 << 5
	FlagIVF         = 1 << 6
	FlagSparse      = 1 << 7
)

// Header is the decoded 100-byte database header (spec 03 §3.2). Field order and
// the offsets below follow the normative table exactly.
type Header struct {
	PageSize        int            // offset 32 (encoded; 65536 stored as 1)
	FormatWrite     byte           // offset 34
	FormatRead      byte           // offset 35
	ChangeCounter   uint32         // offset 36
	PageCount       uint32         // offset 40 (valid iff VersionValidFor==ChangeCounter)
	FreelistTrunk   PageNo         // offset 44
	FreelistCount   uint32         // offset 48
	CatalogRoot     PageNo         // offset 52
	CollectionCount uint32         // offset 56
	SchemaCookie    uint32         // offset 60
	ApplicationID   uint32         // offset 64
	UserVersion     uint32         // offset 68
	Flags           byte           // offset 72
	Encryption      EncryptionKind // offset 73
	TextEncoding    byte           // offset 74 (0 = UTF-8)
	Checksum        ChecksumAlgo   // offset 75
	ReservedPerPage byte           // offset 76
	VersionValidFor uint32         // offset 80
	TxnHighWater    uint64         // offset 84
	HeaderChecksum  uint32         // offset 92
}

// UsableBody returns the number of bytes available for a page body after the
// 32-byte common page header and the per-page reserved tail (spec 03 §6.1).
// The trailing 4-byte page checksum lives inside the reserved/usable accounting
// handled by PageHeader, so this returns the span [PageHeaderSize, end).
func (h *Header) UsableBody() int {
	return h.PageSize - PageHeaderSize - int(h.ReservedPerPage) - PageChecksumSize
}

// NewHeader returns a Header for a freshly created database with the given page
// size and checksum algorithm. The caller sets Flags (e.g. FlagWAL) afterward.
func NewHeader(pageSize int, cks ChecksumAlgo) *Header {
	return &Header{
		PageSize:        pageSize,
		FormatWrite:     FormatLevel,
		FormatRead:      FormatLevel,
		ChangeCounter:   1,
		PageCount:       1, // just page 1 so far
		TextEncoding:    0, // UTF-8
		Checksum:        cks,
		VersionValidFor: 1,
	}
}

// encodePageSize maps a page size to its u16 on-disk code; 65536 is stored as 1
// (spec 03 §3.4).
func encodePageSize(ps int) uint16 {
	if ps == 65536 {
		return 1
	}
	return uint16(ps)
}

// decodePageSize is the inverse of encodePageSize with validation (spec 03 §3.4).
func decodePageSize(code uint16) (int, error) {
	if code == 1 {
		return 65536, nil
	}
	if code < 4096 || code > 32768 || (code&(code-1)) != 0 {
		return 0, ErrCorruptHeader
	}
	return int(code), nil
}

// Encode writes the 100-byte header into buf (which must be at least HeaderSize)
// and stamps the header checksum (spec 03 §3.2, §3.6). It zeroes the magic block
// padding and all reserved bytes.
func (h *Header) Encode(buf []byte) {
	for i := range buf[:HeaderSize] {
		buf[i] = 0
	}
	copy(buf[0:23], magicString)
	// bytes 23..31 remain NUL (magic pad)
	binary.LittleEndian.PutUint16(buf[32:], encodePageSize(h.PageSize))
	buf[34] = h.FormatWrite
	buf[35] = h.FormatRead
	binary.LittleEndian.PutUint32(buf[36:], h.ChangeCounter)
	binary.LittleEndian.PutUint32(buf[40:], h.PageCount)
	binary.LittleEndian.PutUint32(buf[44:], uint32(h.FreelistTrunk))
	binary.LittleEndian.PutUint32(buf[48:], h.FreelistCount)
	binary.LittleEndian.PutUint32(buf[52:], uint32(h.CatalogRoot))
	binary.LittleEndian.PutUint32(buf[56:], h.CollectionCount)
	binary.LittleEndian.PutUint32(buf[60:], h.SchemaCookie)
	binary.LittleEndian.PutUint32(buf[64:], h.ApplicationID)
	binary.LittleEndian.PutUint32(buf[68:], h.UserVersion)
	buf[72] = h.Flags
	buf[73] = byte(h.Encryption)
	buf[74] = h.TextEncoding
	buf[75] = byte(h.Checksum)
	buf[76] = h.ReservedPerPage
	// bytes 77..79 reserved zero
	binary.LittleEndian.PutUint32(buf[80:], h.VersionValidFor)
	binary.LittleEndian.PutUint64(buf[84:], h.TxnHighWater)
	// header_checksum at offset 92 over bytes 0..91
	var sum uint32
	if h.Checksum != ChecksumNone {
		sum = CRC32C(buf[0:92])
	}
	h.HeaderChecksum = sum
	binary.LittleEndian.PutUint32(buf[92:], sum)
	// bytes 96..99 reserved zero
}

// DecodeHeader parses the 100-byte header from buf (which must be at least
// HeaderSize bytes) and validates the magic, page size, and checksum
// (spec 03 §3, §4). It does not interpret feature flags; OpenCheck does that.
func DecodeHeader(buf []byte) (*Header, error) {
	if len(buf) < HeaderSize {
		return nil, ErrShortBuffer
	}
	for i := 0; i < len(magicString); i++ {
		if buf[i] != magicString[i] {
			return nil, ErrNotVecFile
		}
	}
	if buf[len(magicString)] != 0 { // NUL at offset 21? magicString is 21 bytes
		// offset 21 onward must be NUL pad up to 32; first pad byte at 21
		return nil, ErrNotVecFile
	}
	ps, err := decodePageSize(binary.LittleEndian.Uint16(buf[32:]))
	if err != nil {
		return nil, err
	}
	h := &Header{
		PageSize:        ps,
		FormatWrite:     buf[34],
		FormatRead:      buf[35],
		ChangeCounter:   binary.LittleEndian.Uint32(buf[36:]),
		PageCount:       binary.LittleEndian.Uint32(buf[40:]),
		FreelistTrunk:   PageNo(binary.LittleEndian.Uint32(buf[44:])),
		FreelistCount:   binary.LittleEndian.Uint32(buf[48:]),
		CatalogRoot:     PageNo(binary.LittleEndian.Uint32(buf[52:])),
		CollectionCount: binary.LittleEndian.Uint32(buf[56:]),
		SchemaCookie:    binary.LittleEndian.Uint32(buf[60:]),
		ApplicationID:   binary.LittleEndian.Uint32(buf[64:]),
		UserVersion:     binary.LittleEndian.Uint32(buf[68:]),
		Flags:           buf[72],
		Encryption:      EncryptionKind(buf[73]),
		TextEncoding:    buf[74],
		Checksum:        ChecksumAlgo(buf[75]),
		ReservedPerPage: buf[76],
		VersionValidFor: binary.LittleEndian.Uint32(buf[80:]),
		TxnHighWater:    binary.LittleEndian.Uint64(buf[84:]),
		HeaderChecksum:  binary.LittleEndian.Uint32(buf[92:]),
	}
	if h.Checksum != ChecksumNone {
		if got := CRC32C(buf[0:92]); got != h.HeaderChecksum {
			return nil, ErrCorruptHeader
		}
	}
	if h.TextEncoding != 0 {
		return nil, ErrCorruptHeader
	}
	return h, nil
}

// CheckVersion enforces the read/write negotiation rules (spec 03 §4.2). It
// returns (readOnly, error): readOnly is true when the file may be opened only
// for reading because its write level exceeds this build's level.
func (h *Header) CheckVersion() (readOnly bool, err error) {
	if h.FormatRead > FormatLevel {
		return false, ErrVersionTooNew
	}
	if h.FormatWrite > FormatLevel {
		return true, nil
	}
	return false, nil
}

// CheckFeatures rejects must-understand feature flags this build does not
// implement (spec 03 §4.3, §4.4). Encryption and compression are
// must-understand; index-shape flags are may-ignore for a flat reader.
func (h *Header) CheckFeatures() error {
	if h.Flags&FlagEncryption != 0 && h.Encryption != EncNone {
		// Encryption is must-understand; this build does not yet decrypt, so a
		// real key path is required before opening. Reported, not silently read.
		return ErrFeatureNotSupported
	}
	return nil
}
