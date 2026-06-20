package wal

import (
	"testing"

	"github.com/tamnd/vec/vfs"
)

func TestLogCommitRecover(t *testing.T) {
	fs := vfs.NewMem()
	w, err := Create(fs, "t.vec-wal", Options{PageSize: 4096, Sync: SyncFull, Salt: 42})
	if err != nil {
		t.Fatal(err)
	}
	// Batch 1: two op frames + commit.
	if err := w.LogBatch(1, []byte("op-a")); err != nil {
		t.Fatal(err)
	}
	if err := w.LogBatch(1, []byte("op-b")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit(1); err != nil {
		t.Fatal(err)
	}
	// Batch 2: one op frame, NO commit (must be dropped by recovery).
	if err := w.LogBatch(2, []byte("op-c")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	f, _ := fs.Open("t.vec-wal", vfs.OpenRead)
	size, _ := f.Size()
	res, err := Recover(f.ReadAt, size)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Batches) != 2 {
		t.Fatalf("committed batches = %d, want 2 (op-a, op-b)", len(res.Batches))
	}
	if string(res.Batches[0].Encoded) != "op-a" || string(res.Batches[1].Encoded) != "op-b" {
		t.Fatalf("payloads: %q %q", res.Batches[0].Encoded, res.Batches[1].Encoded)
	}
	if res.Salt != 42 {
		t.Fatalf("salt %d", res.Salt)
	}
}

func TestRecoverRejectsTornTail(t *testing.T) {
	fs := vfs.NewMem()
	w, _ := Create(fs, "t.vec-wal", Options{PageSize: 4096, Sync: SyncFull, Salt: 7})
	w.LogBatch(1, []byte("durable"))
	w.Commit(1)
	w.Close()

	// Corrupt the last byte of the file: simulate a torn write past the durable
	// region. The chain must reject everything from the damaged frame on.
	f, _ := fs.Open("t.vec-wal", vfs.OpenReadWrite)
	size, _ := f.Size()
	// Append garbage that does not chain.
	f.WriteAt([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, size)
	size2, _ := f.Size()
	res, err := Recover(f.ReadAt, size2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Batches) != 1 || string(res.Batches[0].Encoded) != "durable" {
		t.Fatalf("durable batch lost or torn tail accepted: %+v", res.Batches)
	}
}

func TestResumeAppendChains(t *testing.T) {
	fs := vfs.NewMem()
	w, _ := Create(fs, "t.vec-wal", Options{PageSize: 4096, Sync: SyncFull, Salt: 99})
	w.LogBatch(1, []byte("first"))
	w.Commit(1)
	w.Close()

	// Reopen via Open, which runs Recover and positions to append.
	w2, res, err := Open(fs, "t.vec-wal", Options{PageSize: 4096, Sync: SyncFull})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Batches) != 1 {
		t.Fatalf("recovered %d batches", len(res.Batches))
	}
	w2.LogBatch(2, []byte("second"))
	w2.Commit(2)
	w2.Close()

	f, _ := fs.Open("t.vec-wal", vfs.OpenRead)
	size, _ := f.Size()
	res2, _ := Recover(f.ReadAt, size)
	if len(res2.Batches) != 2 {
		t.Fatalf("after resume, committed batches = %d, want 2", len(res2.Batches))
	}
	if string(res2.Batches[1].Encoded) != "second" {
		t.Fatalf("resumed frame did not chain: %q", res2.Batches[1].Encoded)
	}
}

func TestCheckpointRotatesSalt(t *testing.T) {
	fs := vfs.NewMem()
	w, _ := Create(fs, "t.vec-wal", Options{PageSize: 4096, Sync: SyncFull, Salt: 5})
	w.LogBatch(1, []byte("x"))
	commitLSN, _ := w.Commit(1)
	saltBefore := w.Salt()
	if err := w.Checkpointed(commitLSN); err != nil {
		t.Fatal(err)
	}
	if w.Salt() == saltBefore {
		t.Fatal("salt did not rotate after checkpoint")
	}
	// After checkpoint the writer rewinds to just past the header for the new
	// generation; a fresh frame must chain under the new salt.
	w.LogBatch(2, []byte("y"))
	w.Commit(2)
	w.Close()

	f, _ := fs.Open("t.vec-wal", vfs.OpenRead)
	size, _ := f.Size()
	res, _ := Recover(f.ReadAt, size)
	if res.Salt != w.Salt() {
		t.Fatalf("recover salt %d != writer salt %d", res.Salt, w.Salt())
	}
	if len(res.Batches) != 1 || string(res.Batches[0].Encoded) != "y" {
		t.Fatalf("post-checkpoint generation recovery wrong: %+v", res.Batches)
	}
}
