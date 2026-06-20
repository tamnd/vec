package vfs

import (
	"bytes"
	"path/filepath"
	"testing"
)

// fsCases runs the same contract over every backend so osfs and memfs stay
// behaviourally identical where it matters.
func runFSContract(t *testing.T, fs FS, path string) {
	t.Helper()
	f, err := fs.Open(path, OpenReadWrite|OpenCreate)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	want := []byte("hello kv")
	if _, err := f.WriteAt(want, 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Sync(SyncFull); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := f.ReadAt(got, 0); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read back %q, want %q", got, want)
	}

	sz, err := f.Size()
	if err != nil || sz != int64(len(want)) {
		t.Fatalf("size = %d, %v", sz, err)
	}

	// Writing past EOF grows the file with a zero gap.
	if _, err := f.WriteAt([]byte("X"), 100); err != nil {
		t.Fatalf("sparse write: %v", err)
	}
	if sz, _ := f.Size(); sz != 101 {
		t.Fatalf("size after sparse write = %d, want 101", sz)
	}

	ok, err := fs.Exists(path)
	if err != nil || !ok {
		t.Fatalf("exists = %v, %v", ok, err)
	}
}

func TestOSContract(t *testing.T) {
	dir := t.TempDir()
	runFSContract(t, NewOS(), filepath.Join(dir, "test.kv"))
}

func TestMemContract(t *testing.T) {
	runFSContract(t, NewMem(), "test.kv")
}

func TestMemCrashRevertsUnsyncedWrites(t *testing.T) {
	fs := NewMem()
	f, _ := fs.Open("db.kv", OpenReadWrite|OpenCreate)
	f.WriteAt([]byte("durable"), 0)
	f.Sync(SyncData)
	f.WriteAt([]byte("LOST!!!"), 0)
	// No sync, then crash: the second write vanishes.
	fs.Crash()

	g, _ := fs.Open("db.kv", OpenReadWrite)
	got := make([]byte, 7)
	g.ReadAt(got, 0)
	if !bytes.Equal(got, []byte("durable")) {
		t.Fatalf("after crash got %q, want durable", got)
	}
}

func TestMemSyncFault(t *testing.T) {
	fs := NewMem()
	f, _ := fs.Open("db.kv", OpenReadWrite|OpenCreate)
	fs.SetSyncFault(1)
	if err := f.Sync(SyncFull); err == nil {
		t.Fatalf("expected injected sync fault")
	}
}

func TestShmMapShared(t *testing.T) {
	fs := NewMem()
	a, err := fs.ShmMap("db.kv", 0, true)
	if err != nil {
		t.Fatalf("shm create: %v", err)
	}
	a[0] = 0x42
	b, err := fs.ShmMap("db.kv", 0, false)
	if err != nil {
		t.Fatalf("shm reopen: %v", err)
	}
	if b[0] != 0x42 {
		t.Fatalf("shm region not shared: got %#x", b[0])
	}
}
