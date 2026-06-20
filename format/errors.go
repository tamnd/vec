// Package format defines the on-disk byte layout of a vec database file: the
// database header, the common page header, page types, varint and record
// encodings, and the page checksum. It is the lowest layer of the stack and has
// no dependencies on the pager, WAL, or any index. Every higher layer agrees on
// the file through the types and constants defined here.
//
// The layout follows specs 02 (data model), 03 (file format), and 04 (storage
// engine) of ~/notes/Spec/2062. Where the prose of spec 03 is internally
// ambiguous (the placement of the page checksum), this package makes a single
// concrete decision and documents it on the relevant type; see PageHeader.
//
// Endianness is little-endian throughout (spec 03 §2.4), matching the dominant
// CPU targets and avoiding a byte-swap on the hot path.
package format

import "errors"

// Sentinel errors returned by the format layer. Higher layers wrap these with
// context; callers compare with errors.Is.
var (
	// ErrNotVecFile means the magic block did not match; the file is not a vec
	// database or its format generation differs (spec 03 §3.3, §4.1).
	ErrNotVecFile = errors.New("vec: not a vec database file")
	// ErrCorruptHeader means the database header failed a structural or checksum
	// check (spec 03 §3.4, §3.6).
	ErrCorruptHeader = errors.New("vec: corrupt database header")
	// ErrCorrupt means a content page failed a structural or checksum check
	// (spec 03 §5.2, §6.2).
	ErrCorrupt = errors.New("vec: corrupt page")
	// ErrVersionTooNew means the file requires a read format level this build
	// does not implement (spec 03 §4.2).
	ErrVersionTooNew = errors.New("vec: file format version too new")
	// ErrFeatureNotSupported means a must-understand feature flag is set that
	// this build does not implement (spec 03 §4.3).
	ErrFeatureNotSupported = errors.New("vec: required file feature not supported")
	// ErrShortBuffer means a decode was given fewer bytes than a record needs.
	ErrShortBuffer = errors.New("vec: short buffer")
)
