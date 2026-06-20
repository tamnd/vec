package index

import (
	"context"
	"sync"
	"testing"

	"github.com/tamnd/vector/distance"
)

// memPageStore is an in-memory PageStore for persistence tests.
type memPageStore struct{ blob []byte }

func (m *memPageStore) PutBlob(b []byte) error {
	m.blob = make([]byte, len(b))
	copy(m.blob, b)
	return nil
}

func (m *memPageStore) GetBlob() ([]byte, error) { return m.blob, nil }

func TestHNSWPersistRecover(t *testing.T) {
	const n, dim, k = 1500, 32, 10
	vecs := randomVectors(n, dim, 31)
	h := buildHNSW(t, vecs, distance.L2Squared, BuildParams{M: 16, EfConstruction: 200, Metric: distance.L2Squared, Seed: 5})
	ctx := context.Background()

	store := &memPageStore{}
	if err := h.Persist(store); err != nil {
		t.Fatal(err)
	}

	h2, _ := NewHNSW(HNSWConfig{Dim: dim, Metric: distance.L2Squared})
	if err := h2.Recover(store); err != nil {
		t.Fatal(err)
	}

	q := randomVectors(20, dim, 41)
	for _, query := range q {
		r1, _ := h.Search(ctx, query, k, nil, SearchParams{EfSearch: 64})
		r2, _ := h2.Search(ctx, query, k, nil, SearchParams{EfSearch: 64})
		if len(r1) != len(r2) {
			t.Fatalf("result count %d != %d after recover", len(r1), len(r2))
		}
		for i := range r1 {
			if r1[i].Position != r2[i].Position {
				t.Fatalf("recovered search differs at %d: %d != %d", i, r1[i].Position, r2[i].Position)
			}
		}
	}
}

func TestHNSWCorruptRecover(t *testing.T) {
	h, _ := NewHNSW(HNSWConfig{Dim: 8, Metric: distance.L2Squared})
	if err := h.Recover(&memPageStore{blob: []byte{1, 2, 3}}); err != ErrIndexCorrupt {
		t.Fatalf("err = %v, want ErrIndexCorrupt", err)
	}
}

func TestHNSWConcurrentSearchInsert(t *testing.T) {
	const dim = 24
	h, _ := NewHNSW(HNSWConfig{Dim: dim, Metric: distance.L2Squared, M: 16, EfConstruction: 100, Seed: 17})
	ctx := context.Background()

	// Seed with some nodes.
	seed := randomVectors(500, dim, 1)
	for i, v := range seed {
		if err := h.Insert(uint32(i), v); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	// Inserters.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			more := randomVectors(200, dim, int64(base+100))
			for i, v := range more {
				_ = h.Insert(uint32(500+base*200+i), v)
			}
		}(w)
	}
	// Searchers.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			qs := randomVectors(200, dim, int64(s+9000))
			for _, q := range qs {
				if _, err := h.Search(ctx, q, 10, nil, SearchParams{EfSearch: 32}); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if h.Stats().NodeCount == 0 {
		t.Fatal("no nodes after concurrent run")
	}
}

func TestFlatPersistRecover(t *testing.T) {
	const n, dim = 300, 16
	vecs := randomVectors(n, dim, 71)
	f := buildFlat(t, vecs, distance.L2Squared)
	store := &memPageStore{}
	if err := f.Persist(store); err != nil {
		t.Fatal(err)
	}
	f2 := NewFlat(dim, distance.L2Squared)
	if err := f2.Recover(store); err != nil {
		t.Fatal(err)
	}
	q := vecs[7]
	r2, _ := f2.Search(context.Background(), q, 3, nil, SearchParams{})
	if r2[0].Position != 7 {
		t.Fatalf("recovered flat nearest = %d, want 7", r2[0].Position)
	}
}
