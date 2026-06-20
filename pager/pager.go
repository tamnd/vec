// Package pager is the layer between the storage and index cores and the file
// (spec 05). It turns page numbers into pinned in-memory frames, owns the buffer
// pool and its replacement policy, allocates and frees pages against the
// freelist, and mediates every read and write to the main file through the vfs
// seam. Nothing above the pager touches bytes on disk; nothing in the pager
// knows what a page means (a vector segment, an HNSW block, a catalog page is the
// caller's concern).
//
// Concurrency in this milestone is single-mutex: correctness first. The
// lock-free sharded read path the spec describes (spec 05) is a later
// optimization that does not change this contract. The pager is shared between
// the kv durability lineage and vec unchanged in shape, adapted only to vec's
// 100-byte header, which carries no engine-kind byte and no checkpoint-LSN field.
package pager

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/tamnd/vector/format"
	"github.com/tamnd/vector/vfs"
)

// Intent declares whether a pinned frame will be read or written.
type Intent int

const (
	// Read pins a frame for reading only.
	Read Intent = iota
	// Write pins a frame the caller intends to mutate.
	Write
)

// Frame is the in-memory home of one page. Its data slice is a window into the
// pool's arena, stable for the pool's lifetime; frames are reused in place.
type Frame struct {
	pgno  uint32
	data  []byte
	pins  atomic.Int32
	dirty bool
	ref   bool // CLOCK reference bit
	slot  int  // index into the pool, -1 if not pooled
}

// PageNo returns the frame's page number.
func (f *Frame) PageNo() uint32 { return f.pgno }

// Data returns the frame's page bytes. The caller must hold a pin and, for
// writes, must have pinned with Write intent and unpin with dirty=true.
func (f *Frame) Data() []byte { return f.data }

// Options configure a pager at open.
type Options struct {
	// PageSize is used only when creating a fresh database; an existing file's
	// page size is read from its header.
	PageSize int
	// CacheFrames is the buffer-pool capacity in frames. Zero selects the default.
	CacheFrames int
	// Checksum is stamped into a fresh file's header.
	Checksum format.ChecksumAlgo
	// Flags are header flag bits for a fresh file (e.g. format.FlagWAL).
	Flags byte
}

const defaultCacheFrames = 2000

// Pager owns the buffer pool and the main file.
type Pager struct {
	fs   vfs.FS
	file vfs.File
	path string

	mu       sync.Mutex
	pageSize int
	header   *format.Header

	index map[uint32]*Frame // resident frames by page number
	pool  []*Frame          // all frames, pooled or free
	arena []byte
	hand  int // CLOCK hand

	dbSize uint32   // page count (high-water mark); pages are 1-based
	free   []uint32 // in-memory freelist, persisted to trunk pages at checkpoint
}

// Create initializes a fresh database file and returns an open pager.
func Create(fs vfs.FS, path string, opts Options) (*Pager, error) {
	ps := opts.PageSize
	if ps == 0 {
		ps = format.DefaultPageSize
	}
	if !format.ValidPageSize(ps) {
		return nil, format.ErrBadPageSize
	}
	f, err := fs.Open(path, vfs.OpenReadWrite|vfs.OpenCreate)
	if err != nil {
		return nil, err
	}
	checksum := opts.Checksum
	h := format.NewHeader(ps, checksum)
	h.Flags = opts.Flags
	p := newPager(fs, f, path, ps, opts.CacheFrames)
	p.header = h
	p.dbSize = 1
	// Write page 1 (the header page) so the file is non-empty and valid.
	page1 := make([]byte, ps)
	h.Encode(page1)
	if _, err := f.WriteAt(page1, 0); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(vfs.SyncFull); err != nil {
		f.Close()
		return nil, err
	}
	return p, nil
}

// Open opens an existing database file and returns a pager. It reads and
// validates the header from page 1 and loads the freelist.
func Open(fs vfs.FS, path string, opts Options) (*Pager, error) {
	f, err := fs.Open(path, vfs.OpenReadWrite)
	if err != nil {
		return nil, err
	}
	hbuf := make([]byte, format.HeaderSize)
	if _, err := f.ReadAt(hbuf, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("pager: read header: %w", err)
	}
	h, err := format.DecodeHeader(hbuf)
	if err != nil {
		f.Close()
		return nil, err
	}
	size, err := f.Size()
	if err != nil {
		f.Close()
		return nil, err
	}
	ps := h.PageSize
	p := newPager(fs, f, path, ps, opts.CacheFrames)
	p.header = h
	p.dbSize = uint32(size / int64(ps))
	if p.dbSize == 0 {
		p.dbSize = 1
	}
	if err := p.loadFreelist(); err != nil {
		f.Close()
		return nil, err
	}
	return p, nil
}

func newPager(fs vfs.FS, f vfs.File, path string, pageSize, cacheFrames int) *Pager {
	if cacheFrames <= 0 {
		cacheFrames = defaultCacheFrames
	}
	p := &Pager{
		fs:       fs,
		file:     f,
		path:     path,
		pageSize: pageSize,
		index:    make(map[uint32]*Frame, cacheFrames),
		pool:     make([]*Frame, 0, cacheFrames),
		arena:    make([]byte, cacheFrames*pageSize),
	}
	for i := 0; i < cacheFrames; i++ {
		fr := &Frame{slot: i, data: p.arena[i*pageSize : (i+1)*pageSize : (i+1)*pageSize]}
		p.pool = append(p.pool, fr)
	}
	return p
}

// PageSize reports the on-disk page size in bytes.
func (p *Pager) PageSize() int { return p.pageSize }

// Header returns the live header. Callers must not retain it across Checkpoint.
func (p *Pager) Header() *format.Header { return p.header }

// DBSize reports the current page count (high-water mark).
func (p *Pager) DBSize() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dbSize
}

// FreeCount reports how many pages are currently on the in-memory freelist,
// available for reallocation before the file grows (spec 04).
func (p *Pager) FreeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.free)
}
