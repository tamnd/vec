package wal

import (
	"encoding/binary"
	"io"
)

// Frame is one decoded WAL frame yielded by the reader during recovery.
type Frame struct {
	Type    FrameType
	LSN     uint64
	Version uint64
	Payload []byte
}

// CommittedBatch is an operation batch whose commit frame was found durable. The
// recovery driver in the db layer replays these in LSN order through its Apply
// path (spec 05, spec 06).
type CommittedBatch struct {
	Version uint64
	LSN     uint64 // the op-batch frame's LSN
	Encoded []byte // serialized operation batch, decoded by the db layer
}

// RecoverResult summarizes a recovery scan.
type RecoverResult struct {
	// Batches are the committed kv-batches in LSN order, ready to replay.
	Batches []CommittedBatch
	// LastCheckpointLSN is the highest foldedLSN recorded by a durable checkpoint
	// frame, or 0 if none. Frames at or before it are already in the main file.
	LastCheckpointLSN uint64
	// DurableLSN is the LSN of the last frame that chained correctly; the tail is
	// torn or stale beyond it.
	DurableLSN uint64
	// DurableEndOff is the file offset just past the last frame that chained
	// correctly -- the point a resumed writer appends from, overwriting any torn
	// or stale tail (used by wal.Open).
	DurableEndOff int64
	// DurableSum is the running chained checksum at DurableEndOff, the seed the
	// resumed writer's next frame chains from.
	DurableSum uint64
	// Salt is the WAL generation's salt read from the header.
	Salt uint64
	// TornTail is true if the scan stopped at a frame that failed the chain,
	// meaning the file held bytes past the durable region.
	TornTail bool
}

// Recover walks the -wal file from its header, verifies the chained, salted
// checksum frame by frame, and returns the committed batches in the durable
// region. The first frame that fails the chain or carries a stale salt ends the
// durable log; everything past it is discarded as torn or left over from a
// previous generation (spec 05). A batch counts as committed only if a
// checksum-valid commit frame for it is reached (spec 05); a trailing batch
// with no commit frame is dropped.
//
// readAt reads exactly len(p) bytes at off, or fewer at EOF; it mirrors
// vfs.File.ReadAt semantics. Passing the WAL file's ReadAt keeps this decoupled
// from the vfs package.
func Recover(readAt func(p []byte, off int64) (int, error), size int64) (RecoverResult, error) {
	var res RecoverResult
	if size < headerSize {
		// No header yet: an empty or never-written WAL. Nothing to recover.
		return res, nil
	}
	hdr := make([]byte, headerSize)
	if _, err := readAt(hdr, 0); err != nil && err != io.EOF {
		return res, err
	}
	if binary.BigEndian.Uint32(hdr[0:4]) != walMagic {
		// Not a kv WAL (or zeroed): treat as empty, nothing committed.
		return res, nil
	}
	if got, want := binary.BigEndian.Uint64(hdr[24:32]), sum64(hdr[:24]); got != want {
		// Header checksum bad: the WAL is unreadable, recover nothing.
		return res, nil
	}
	salt := binary.BigEndian.Uint64(hdr[12:20])
	res.Salt = salt

	// Seed the chain from the header checksum, matching the writer.
	prevSum := binary.BigEndian.Uint64(hdr[24:32])
	// With no durable frames the resume point is just past the header, chaining
	// from the header checksum.
	res.DurableEndOff = int64(headerSize)
	res.DurableSum = prevSum

	off := int64(headerSize)
	var pending []CommittedBatch // batches logged but not yet committed
	hp := make([]byte, frameHeaderSize)
	for off+frameHeaderSize <= size {
		if _, err := readAt(hp, off); err != nil && err != io.EOF {
			return res, err
		}
		ft := FrameType(hp[0])
		plen := binary.BigEndian.Uint32(hp[1:5])
		lsn := binary.BigEndian.Uint64(hp[5:13])
		version := binary.BigEndian.Uint64(hp[13:21])
		fsalt := binary.BigEndian.Uint64(hp[21:29])
		sum := binary.BigEndian.Uint64(hp[29:37])

		end := off + int64(frameHeaderSize) + int64(plen)
		if end > size {
			// Truncated payload: torn tail.
			res.TornTail = true
			break
		}
		payload := make([]byte, plen)
		if plen > 0 {
			if _, err := readAt(payload, off+int64(frameHeaderSize)); err != nil && err != io.EOF {
				return res, err
			}
		}
		// Verify salt generation and the chained checksum.
		if fsalt != salt || chain(prevSum, hp[0:29], payload) != sum {
			res.TornTail = true
			break
		}

		switch ft {
		case FrameOpBatch:
			pending = append(pending, CommittedBatch{Version: version, LSN: lsn, Encoded: payload})
		case FrameCommit:
			// Promote every pending batch to committed.
			res.Batches = append(res.Batches, pending...)
			pending = pending[:0]
		case FrameCheckpoint:
			res.LastCheckpointLSN = binary.BigEndian.Uint64(payload[:8])
			// A checkpoint frame ends a generation; the salt rotates after it. In a
			// single-generation scan we simply note the boundary and continue: any
			// frames after it belong to the next generation and will mismatch this
			// salt, naturally ending the walk.
		case FramePageImage:
			// Reserved; ignored by the logical redo path in this milestone.
		}

		prevSum = sum
		res.DurableLSN = lsn
		off = end
		res.DurableEndOff = off
		res.DurableSum = prevSum
	}
	// pending (uncommitted trailing) batches are dropped: not durable.
	return res, nil
}

// CommittedAfter returns the committed batches with an LSN strictly greater than
// lsn, i.e. those not yet folded into the main file at the given checkpoint
// boundary. The recovery driver replays exactly these.
func (r RecoverResult) CommittedAfter(lsn uint64) []CommittedBatch {
	var out []CommittedBatch
	for _, b := range r.Batches {
		if b.LSN > lsn {
			out = append(out, b)
		}
	}
	return out
}
