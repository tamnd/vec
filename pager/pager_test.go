package pager

import (
	"testing"

	"github.com/tamnd/vector/format"
	"github.com/tamnd/vector/vfs"
)

func TestCreateOpenRoundTrip(t *testing.T) {
	fs := vfs.NewMem()
	p, err := Create(fs, "t.vec", Options{PageSize: 4096, Checksum: format.ChecksumCRC32C, Flags: format.FlagWAL})
	if err != nil {
		t.Fatal(err)
	}
	if p.PageSize() != 4096 {
		t.Fatalf("page size %d", p.PageSize())
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p2, err := Open(fs, "t.vec", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Header().PageSize != 4096 || p2.Header().Flags&format.FlagWAL == 0 {
		t.Fatalf("header not preserved: %+v", p2.Header())
	}
	p2.Close()
}

func TestAllocateGetPersist(t *testing.T) {
	fs := vfs.NewMem()
	p, err := Create(fs, "t.vec", Options{PageSize: 4096, Checksum: format.ChecksumCRC32C})
	if err != nil {
		t.Fatal(err)
	}
	pgno, fr, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	h := format.PageHeader{Type: format.PageVectorSegment, CellCount: 5}
	h.Encode(fr.Data())
	copy(fr.Data()[format.PageHeaderSize:], []byte("vector payload"))
	p.Unpin(fr, true)
	if err := p.Checkpoint(0); err != nil {
		t.Fatal(err)
	}
	p.Close()

	p2, err := Open(fs, "t.vec", Options{})
	if err != nil {
		t.Fatal(err)
	}
	fr2, err := p2.Get(pgno, Read)
	if err != nil {
		t.Fatal(err)
	}
	if err := format.VerifyPageChecksum(fr2.Data(), p2.Header().Checksum); err != nil {
		t.Fatalf("checksum: %v", err)
	}
	got, _ := format.DecodePageHeader(fr2.Data())
	if got.Type != format.PageVectorSegment || got.CellCount != 5 {
		t.Fatalf("page header mismatch: %+v", got)
	}
	if string(fr2.Data()[format.PageHeaderSize:format.PageHeaderSize+14]) != "vector payload" {
		t.Fatal("payload mismatch")
	}
	p2.Unpin(fr2, false)
	p2.Close()
}

func TestFreelistReuseAndPersist(t *testing.T) {
	fs := vfs.NewMem()
	p, err := Create(fs, "t.vec", Options{PageSize: 4096, Checksum: format.ChecksumCRC32C})
	if err != nil {
		t.Fatal(err)
	}
	// Allocate several pages, free some, checkpoint, reopen, confirm freelist.
	var pages []uint32
	for i := 0; i < 5; i++ {
		pgno, fr, err := p.Allocate()
		if err != nil {
			t.Fatal(err)
		}
		p.Unpin(fr, true)
		pages = append(pages, pgno)
	}
	p.Free(pages[1])
	p.Free(pages[3])
	if p.FreeCount() != 2 {
		t.Fatalf("free count %d", p.FreeCount())
	}
	if err := p.Checkpoint(0); err != nil {
		t.Fatal(err)
	}
	p.Close()

	p2, err := Open(fs, "t.vec", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p2.FreeCount() != 2 {
		t.Fatalf("reopened free count %d, want 2", p2.FreeCount())
	}
	// Next allocation should reuse a freed page, not grow the file.
	before := p2.DBSize()
	pgno, fr, err := p2.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	p2.Unpin(fr, true)
	if pgno != pages[3] && pgno != pages[1] {
		t.Fatalf("did not reuse a freed page: got %d", pgno)
	}
	if p2.DBSize() != before {
		t.Fatalf("file grew despite free pages: %d -> %d", before, p2.DBSize())
	}
	p2.Close()
}
