package query

// arenaBlockSize is the default block the arena grows by (spec 10 §14.2).
const arenaBlockSize = 64 << 10

// QueryArena is the per-query scratch allocator (spec 10 §14.2). It hands out
// transient backing memory for candidate slices, rerank buffers, and fusion maps,
// then is dropped whole at query end so the GC reclaims it in one shot rather than
// per object. A non-zero Limit caps total bytes and trips ErrQueryMemoryExceeded
// (spec 10 §14.3), which the executor recovers into an error.
type QueryArena struct {
	cur   []byte
	limit int64
	used  int64
}

// newQueryArena returns an arena bounded by limit bytes; limit <= 0 is unlimited.
func newQueryArena(limit int64) *QueryArena {
	return &QueryArena{limit: limit}
}

// alloc returns an n-byte slice, growing the arena if needed. It panics with
// ErrQueryMemoryExceeded when the limit is crossed (spec 10 §14.2 Alloc).
func (a *QueryArena) alloc(n int) []byte {
	if len(a.cur) < n {
		block := arenaBlockSize
		if n > block {
			block = n
		}
		a.cur = make([]byte, block)
	}
	p := a.cur[:n:n]
	a.cur = a.cur[n:]
	a.used += int64(n)
	if a.limit > 0 && a.used > a.limit {
		panic(ErrQueryMemoryExceeded)
	}
	return p
}

// Used reports the bytes handed out so far (spec 10 §18.3 ArenaUsedBytes).
func (a *QueryArena) Used() int64 { return a.used }
