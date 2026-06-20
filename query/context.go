package query

import (
	"context"
	"sync"

	"github.com/tamnd/vec/distance"
	"github.com/tamnd/vec/index"
	"github.com/tamnd/vec/storage"
)

// Collection is the executor's binding to one collection's physical resources
// (spec 10 §18.1): the storage engine, the primary ANN index (nil for flat-only),
// the vector geometry, and the metadata column-id map. The db layer ([14], task 15)
// builds it from a catalog.Collection plus the index handle; the planner reads its
// shape to choose an access path and the executor reads it to run operators.
type Collection struct {
	Engine *storage.Engine
	CollID uint64
	Dims   int
	Metric distance.Metric

	// Index is the primary ANN index over the vector column, or nil when only a
	// flat brute-force scan is available (spec 10 §2.4).
	Index index.Index
	// IndexKind tells the planner which cost formula applies (spec 13 §6).
	IndexKind PathKind
	// HNSW build parameters, for the analytical recall fallback (spec 13 §6.4).
	M, EfConstruction int
	// NList is the IVF cell count, for nprobe selection (spec 13 §6.5).
	NList int

	// MetaCols maps a metadata column name to its engine column id, for projection.
	MetaCols map[string]storage.ColID
}

// colID resolves a projected column name to its engine id.
func (c *Collection) colID(name string) (storage.ColID, bool) {
	id, ok := c.MetaCols[name]
	return id, ok
}

// QueryStats accumulates per-operator counters over one execution (spec 10 §18.3).
// The executor returns it on the ResultSet and EXPLAIN ANALYZE renders it.
type QueryStats struct {
	ANNCandidatesReturned  int64
	ANNRetries             int32
	PreFilterBitmapSize    int64
	PostFilterEvaluated    int64
	PostFilterSurvived     int64
	RerankCandidates       int32
	FlatScanned            int64
	BM25CandidatesReturned int64
	FuseCombined           int64
	ArenaUsedBytes         int64
}

// ExecContext carries everything an operator needs that is not in the plan (spec
// 10 §18.1): cancellation, the MVCC snapshot, the scratch arena, the shared worker
// pool, the stats sink, and the storage binding.
type ExecContext struct {
	Ctx      context.Context
	Snapshot storage.Snapshot
	Arena    *QueryArena
	Pool     *WorkerPool
	Stats    *QueryStats
	Coll     *Collection
}

// WorkerPool is the shared, bounded goroutine pool for intra-query parallelism
// (spec 10 §12.5). Size defaults to GOMAXPROCS at the executor level.
type WorkerPool struct {
	workers int
}

// NewWorkerPool returns a pool that runs up to n concurrent jobs (spec 10 §12.5).
func NewWorkerPool(n int) *WorkerPool {
	if n < 1 {
		n = 1
	}
	return &WorkerPool{workers: n}
}

// Workers returns the configured parallelism degree.
func (p *WorkerPool) Workers() int { return p.workers }

// run executes jobs across at most p.workers goroutines and blocks until all
// finish. Each job receives its index. This is the morsel dispatcher for the
// parallel flat scan (spec 10 §12.2).
func (p *WorkerPool) run(n int, job func(i int)) {
	if n <= 0 {
		return
	}
	if p.workers <= 1 || n == 1 {
		for i := 0; i < n; i++ {
			job(i)
		}
		return
	}
	var (
		wg   sync.WaitGroup
		next = make(chan int)
	)
	workers := p.workers
	if workers > n {
		workers = n
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				job(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}

// bitmapFilter adapts a storage.PositionBitmap to the index.Bitmap interface so a
// pre-filter set can be handed straight to index.Search (spec 10 §9.2). A nil
// bitmap means no filter.
type bitmapFilter struct {
	bm *storage.PositionBitmap
}

func (f bitmapFilter) Contains(pos uint32) bool { return f.bm.Contains(pos) }
func (f bitmapFilter) Count() int               { return f.bm.Count() }
