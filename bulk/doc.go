// Package bulk implements the bulk import, logical dump/load, and backup-format
// pieces of spec 17 on top of the public vec facade.
//
// The package splits into four parts:
//
//   - Import: streaming readers for the common vector file formats (CSV, JSON,
//     JSONL, fbin/fvecs/ivecs/bvecs, npy) behind a single RowSource interface, and
//     a morsel-batched Import driver that maps rows onto a collection and writes
//     them through Collection.UpsertBatch.
//   - Dump and load: a VectorSQL-text logical export of a database (schema DDL plus
//     batched INSERTs) and the symmetric Load that rebuilds a database from it. This
//     is the portable, version-independent migration path.
//   - WAL segment codec: the on-disk WAL-segment format from spec 17 Appendix B,
//     used by incremental and streaming backup. The codec is independent of the live
//     engine: it encodes, decodes, CRC-checks, and detects a torn tail.
//   - Backup destinations: a small BackupDestination interface plus a local-directory
//     implementation and the generation manifest model, so object-store backends are
//     one-file additions.
//
// The vec engine in this build is process-resident, so the physical page-level
// backup, live WAL streaming, point-in-time replay into a running file, and
// read-replica apply paths described in spec 17 sections 4 through 8 are gated on
// the on-disk pager and WAL wiring landing in the facade. This package implements
// the formats and the orchestration those paths build on, and the logical
// dump/load path that works against the current engine end to end. The import note
// at notes/Spec/2062/implementation/17-bulk.md records exactly what is wired and
// what waits.
package bulk
