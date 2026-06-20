package hybrid

import (
	"math"
	"sort"
	"sync"
)

// ScoredPos is one ranked result: a dense storage position and a relevance score
// where larger is better (spec 11 §9.5). Every ranker in this package (BM25, sparse,
// MaxSim) and the fuser produce ScoredPos lists, so they compose uniformly.
type ScoredPos struct {
	Pos   uint32
	Score float64
}

// Bitmap is the read-only position-membership view a filtered query passes to a
// ranker so scoring skips non-matching documents (spec 11 §10.6). storage's
// PositionBitmap satisfies it.
type Bitmap interface {
	Contains(pos uint32) bool
}

// posting is one entry in a term's posting list: the document position and the term
// frequency in that document (spec 11 §9.2).
type posting struct {
	pos uint32
	tf  uint16
}

// termEntry is the dictionary record for a term: its posting list and document
// frequency (spec 11 §9.2).
type termEntry struct {
	postings []posting
	df       uint32
}

// BM25Params are the BM25 tunables (spec 11 §9.4). Defaults follow Robertson 1994.
type BM25Params struct {
	K1 float64 // term-frequency saturation, default 1.2
	B  float64 // length normalization, default 0.75
}

// DefaultBM25Params returns the Robertson 1994 / Elasticsearch defaults.
func DefaultBM25Params() BM25Params { return BM25Params{K1: 1.2, B: 0.75} }

// BM25Index is the in-memory inverted index and BM25 ranker over one or more TEXT
// columns (spec 11 §9). Documents are keyed by dense storage position. It supports
// incremental add and delete, optional per-field weighting, and full
// document-at-a-time scoring.
type BM25Index struct {
	mu       sync.RWMutex
	tok      Tokenizer
	params   BM25Params
	dict     map[string]*termEntry
	docLen   map[uint32]int // position -> token count (length-weighted)
	deleted  map[uint32]struct{}
	totalLen int64
	docCount int
	fieldWts []float64 // per-field weights, parallel to AddDoc fields; nil = all 1.0
}

// NewBM25Index returns an empty index using tok and params. A nil tokenizer uses the
// standard tokenizer; zero params use the defaults.
func NewBM25Index(tok Tokenizer, params BM25Params) *BM25Index {
	if tok == nil {
		tok = NewStandardTokenizer()
	}
	if params.K1 == 0 {
		params.K1 = 1.2
	}
	if params.B == 0 {
		params.B = 0.75
	}
	return &BM25Index{
		tok:     tok,
		params:  params,
		dict:    make(map[string]*termEntry),
		docLen:  make(map[uint32]int),
		deleted: make(map[uint32]struct{}),
	}
}

// SetFieldWeights configures per-field weights for multi-column indexing (spec 11
// §9.7). The weights line up with the fields passed to AddDocFields.
func (idx *BM25Index) SetFieldWeights(weights []float64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.fieldWts = append([]float64(nil), weights...)
}

// AddDoc indexes a single-field document at position pos.
func (idx *BM25Index) AddDoc(pos uint32, text string) {
	idx.AddDocFields(pos, []string{text})
}

// AddDocFields indexes a multi-field document at pos. Each field's terms contribute
// term frequencies scaled by that field's weight (spec 11 §9.7); the document length
// is the weighted token count, matching the field-weighted BM25 normalization.
func (idx *BM25Index) AddDocFields(pos uint32, fields []string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// A re-add replaces the prior document at pos.
	idx.removeLocked(pos)

	weightedTF := make(map[string]float64)
	for fi, field := range fields {
		w := 1.0
		if fi < len(idx.fieldWts) {
			w = idx.fieldWts[fi]
		}
		for _, term := range idx.tok.Tokenize(field) {
			weightedTF[term] += w
		}
	}
	if len(weightedTF) == 0 {
		// An empty document still counts toward the corpus size and avgdl with length 0.
		idx.docLen[pos] = 0
		idx.docCount++
		return
	}

	dl := 0
	for term, wtf := range weightedTF {
		tf := int(math.Round(wtf))
		if tf < 1 {
			tf = 1
		}
		if tf > math.MaxUint16 {
			tf = math.MaxUint16
		}
		dl += tf
		e := idx.dict[term]
		if e == nil {
			e = &termEntry{}
			idx.dict[term] = e
		}
		e.postings = append(e.postings, posting{pos: pos, tf: uint16(tf)})
		e.df++
	}
	idx.docLen[pos] = dl
	idx.totalLen += int64(dl)
	idx.docCount++
}

// Remove drops the document at pos from the index (spec 11 §9.6).
func (idx *BM25Index) Remove(pos uint32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.removeLocked(pos)
}

func (idx *BM25Index) removeLocked(pos uint32) {
	dl, ok := idx.docLen[pos]
	if !ok {
		return
	}
	// Mark deleted; posting entries are pruned lazily during search and physically on
	// the next compaction. Document statistics are corrected immediately so scoring
	// stays accurate (spec 11 §9.6).
	idx.deleted[pos] = struct{}{}
	idx.totalLen -= int64(dl)
	idx.docCount--
	delete(idx.docLen, pos)
}

// avgDocLen returns the corpus average document length, or 1 when empty (spec 11
// §9.4 avgdl).
func (idx *BM25Index) avgDocLen() float64 {
	if idx.docCount == 0 {
		return 1
	}
	return float64(idx.totalLen) / float64(idx.docCount)
}

// idf is the Robertson/Sparck-Jones IDF with +1 smoothing (spec 11 §9.4).
func idf(n, df int) float64 {
	return math.Log((float64(n)-float64(df)+0.5)/(float64(df)+0.5) + 1)
}

// Search returns the top-k positions by BM25 score for the query string, using
// document-at-a-time accumulation over the query terms' posting lists (spec 11 §9.5).
// A non-nil filter restricts scoring to positions it contains (spec 11 §10.6).
func (idx *BM25Index) Search(query string, k int, filter Bitmap) []ScoredPos {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if idx.docCount == 0 || k <= 0 {
		return nil
	}
	terms := dedup(idx.tok.Tokenize(query))
	avgdl := idx.avgDocLen()
	k1, b := idx.params.K1, idx.params.B

	scores := make(map[uint32]float64)
	for _, term := range terms {
		e := idx.dict[term]
		if e == nil {
			continue // term not in the index contributes 0 to every document
		}
		termIDF := idf(idx.docCount, int(e.df))
		for _, p := range e.postings {
			if _, dead := idx.deleted[p.pos]; dead {
				continue
			}
			if filter != nil && !filter.Contains(p.pos) {
				continue
			}
			tf := float64(p.tf)
			dl := float64(idx.docLen[p.pos])
			norm := tf * (k1 + 1) / (tf + k1*(1-b+b*dl/avgdl))
			scores[p.pos] += termIDF * norm
		}
	}
	return topKByScore(scores, k)
}

// DocCount reports the number of live indexed documents.
func (idx *BM25Index) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.docCount
}

// topKByScore returns the highest-scoring positions, breaking ties by smaller
// position so the ranking is deterministic (spec 11 §9.5, spec 10 §20.7).
func topKByScore(scores map[uint32]float64, k int) []ScoredPos {
	out := make([]ScoredPos, 0, len(scores))
	for pos, s := range scores {
		out = append(out, ScoredPos{Pos: pos, Score: s})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Pos < out[j].Pos
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}
