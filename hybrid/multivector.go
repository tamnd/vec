package hybrid

import (
	"context"
	"math"

	"github.com/tamnd/vec/distance"
	"github.com/tamnd/vec/index"
)

// MultiVecIndex implements approximate ColBERT MaxSim retrieval (spec 11 §12). A
// document is a set of per-token vectors; relevance is the late-interaction MaxSim
// score: for each query token, the best dot product against any document token,
// summed over query tokens. Exact MaxSim over a corpus is infeasible (§12.3), so this
// follows PLAID: a token-level ANN index generates candidate documents, then each
// candidate is exactly re-scored.
type MultiVecIndex struct {
	dim        int
	tokenIndex index.Index       // one point per document token, keyed by token id
	tokenToDoc map[uint32]uint32 // token id -> document position
	docTokens  map[uint32][][]float32
	nextToken  uint32
}

// NewMultiVecIndex returns an empty multi-vector index. tokenIndex is the token-level
// ANN index used for candidate generation; pass an index.Flat for exact candidates or
// an index.HNSW for scale (spec 11 §12.3). A nil tokenIndex uses an internal flat
// index over dot-product similarity.
func NewMultiVecIndex(dim int, tokenIndex index.Index) *MultiVecIndex {
	if tokenIndex == nil {
		tokenIndex = index.NewFlat(dim, distance.Dot)
	}
	return &MultiVecIndex{
		dim:        dim,
		tokenIndex: tokenIndex,
		tokenToDoc: make(map[uint32]uint32),
		docTokens:  make(map[uint32][][]float32),
	}
}

// AddDoc indexes a document's token matrix at position pos. Each token becomes one
// point in the token-level index annotated with its parent document (spec 11 §12.3).
func (idx *MultiVecIndex) AddDoc(pos uint32, tokens [][]float32) error {
	stored := make([][]float32, len(tokens))
	for i, t := range tokens {
		cp := make([]float32, len(t))
		copy(cp, t)
		stored[i] = cp
		tid := idx.nextToken
		idx.nextToken++
		idx.tokenToDoc[tid] = pos
		if err := idx.tokenIndex.Insert(tid, cp); err != nil {
			return err
		}
	}
	idx.docTokens[pos] = stored
	return nil
}

// Search performs approximate MaxSim retrieval (spec 11 §12.3): phase one probes the
// token index with each query token to gather candidate documents, phase two computes
// exact MaxSim over each candidate's full token matrix and keeps the top-k. perToken
// is the candidate count C per query token; a non-nil filter restricts candidates to
// contained document positions.
func (idx *MultiVecIndex) Search(ctx context.Context, queryTokens [][]float32, k, perToken int, filter Bitmap) ([]ScoredPos, error) {
	if k <= 0 || len(queryTokens) == 0 {
		return nil, nil
	}
	if perToken <= 0 {
		perToken = 100
	}

	// Phase 1: candidate generation. Union the documents whose tokens are nearest to
	// any query token.
	candidates := make(map[uint32]struct{})
	for _, qt := range queryTokens {
		hits, err := idx.tokenIndex.Search(ctx, qt, perToken, nil, index.SearchParams{MaxCandidates: perToken})
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			doc, ok := idx.tokenToDoc[h.Position]
			if !ok {
				continue
			}
			if filter != nil && !filter.Contains(doc) {
				continue
			}
			candidates[doc] = struct{}{}
		}
	}

	// Phase 2: exact MaxSim rerank over the candidate documents.
	scores := make(map[uint32]float64, len(candidates))
	for doc := range candidates {
		scores[doc] = maxSim(queryTokens, idx.docTokens[doc])
	}
	return topKByScore(scores, k), nil
}

// maxSim is the ColBERT late-interaction score (spec 11 §12.1): the sum over query
// tokens of the maximum dot product against any document token.
func maxSim(queryTokens, docTokens [][]float32) float64 {
	total := 0.0
	for _, q := range queryTokens {
		best := math.Inf(-1)
		for _, d := range docTokens {
			s := float64(distance.DotFloat32(q, d))
			if s > best {
				best = s
			}
		}
		if math.IsInf(best, -1) {
			best = 0
		}
		total += best
	}
	return total
}

// DocCount reports the number of indexed documents.
func (idx *MultiVecIndex) DocCount() int { return len(idx.docTokens) }
