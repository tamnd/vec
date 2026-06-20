package format

import "hash/crc32"

// crc32cTable is the Castagnoli (CRC32C) table, polynomial 0x1EDC6F41
// (spec 03 §6.2). The standard library uses the hardware CRC32 instruction
// (SSE4.2 / ARMv8 CRC) automatically when the platform provides it.
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// CRC32C returns the Castagnoli CRC32 of b. It is the default page and WAL frame
// checksum (spec 03 §6.2, spec 05).
func CRC32C(b []byte) uint32 {
	return crc32.Checksum(b, crc32cTable)
}

// CRC32CUpdate continues a running CRC32C over an additional slice; used when a
// checksum spans non-contiguous regions of a page.
func CRC32CUpdate(crc uint32, b []byte) uint32 {
	return crc32.Update(crc, crc32cTable, b)
}
