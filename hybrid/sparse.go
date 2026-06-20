package hybrid

import "sort"

// SparsePair is one non-zero dimension of a sparse vector: the vocabulary index and
// its learned weight (spec 11 §11.2). A sparse vector is a slice of these sorted by
// index.
type SparsePair struct {
	Index uint32
	Value float32
}

// SortSparse sorts a sparse vector by index in place and returns it, the canonical
// form vec stores (spec 11 §11.2).
func SortSparse(v []SparsePair) []SparsePair {
	sort.Slice(v, func(i, j int) bool { return v[i].Index < v[j].Index })
	return v
}

// sparsePosting is one entry of a sparse dimension's posting list: the document
// position and that document's weight on the dimension (spec 11 §11.3).
type sparsePosting struct {
	pos    uint32
	weight float32
}

// SparseIndex is the inverted index for learned-sparse (SPLADE) retrieval (spec 11
// §11.3). It mirrors the BM25 inverted index but stores per-dimension float weights
// instead of term frequencies, and scores by sparse dot product instead of BM25.
type SparseIndex struct {
	postings map[uint32][]sparsePosting // dimension -> documents with a weight there
	deleted  map[uint32]struct{}
	docCount int
}

// NewSparseIndex returns an empty sparse index.
func NewSparseIndex() *SparseIndex {
	return &SparseIndex{
		postings: make(map[uint32][]sparsePosting),
		deleted:  make(map[uint32]struct{}),
	}
}

// AddDoc indexes a document's sparse vector at position pos. Re-adding replaces the
// prior vector at that position.
func (idx *SparseIndex) AddDoc(pos uint32, vec []SparsePair) {
	idx.Remove(pos)
	delete(idx.deleted, pos)
	for _, p := range vec {
		if p.Value == 0 {
			continue
		}
		idx.postings[p.Index] = append(idx.postings[p.Index], sparsePosting{pos: pos, weight: p.Value})
	}
	idx.docCount++
}

// Remove drops the document at pos.
func (idx *SparseIndex) Remove(pos uint32) {
	if _, ok := idx.deleted[pos]; ok {
		return
	}
	// Lazily tombstone; the scorer skips deleted positions and compaction prunes them.
	// docCount is only decremented for documents previously added.
	for dim, list := range idx.postings {
		for _, sp := range list {
			if sp.pos == pos {
				idx.deleted[pos] = struct{}{}
				idx.docCount--
				_ = dim
				return
			}
		}
	}
}

// Search computes the sparse dot product between the query vector and every indexed
// document, returning the top-k positions (spec 11 §11.3). For each non-zero query
// dimension it accumulates query_weight * doc_weight over that dimension's posting
// list. A non-nil filter restricts scoring to contained positions.
func (idx *SparseIndex) Search(query []SparsePair, k int, filter Bitmap) []ScoredPos {
	if k <= 0 {
		return nil
	}
	scores := make(map[uint32]float64)
	for _, qp := range query {
		if qp.Value == 0 {
			continue
		}
		for _, dp := range idx.postings[qp.Index] {
			if _, dead := idx.deleted[dp.pos]; dead {
				continue
			}
			if filter != nil && !filter.Contains(dp.pos) {
				continue
			}
			scores[dp.pos] += float64(qp.Value) * float64(dp.weight)
		}
	}
	return topKByScore(scores, k)
}

// DocCount reports the number of live indexed documents.
func (idx *SparseIndex) DocCount() int { return idx.docCount }
