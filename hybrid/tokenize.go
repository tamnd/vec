// Package hybrid implements vec's lexical and multi-modal retrieval layer
// (spec 11): the BM25 keyword index (§9), reciprocal-rank and score-normalized
// fusion (§10), learned-sparse (SPLADE) dot-product search (§11), and multi-vector
// (ColBERT) MaxSim late interaction (§12). The metadata-predicate filter strategies
// of §4-§7 live in the query package's planner and executor; this package supplies
// the non-dense ranking signals those queries fuse with.
//
// Everything here is in-memory and position-keyed, the same shape the index package
// uses: documents are identified by their dense storage position so a hybrid result
// fuses directly with a dense ANN result and resolves to a point id through the same
// late-materialization path (spec 10 §7). The on-disk keyword-index section of the
// file format ([03] §13) is a later persistence slice; the search algorithms are
// identical whether the postings come from memory or a page.
package hybrid

import (
	"strings"
	"unicode"
)

// Tokenizer converts a text value into a sequence of terms (spec 11 §9.3). It is
// pluggable at the library API level; the tokenizer choice is fixed for the life of
// an index because build-time and query-time tokenization must agree.
type Tokenizer interface {
	Tokenize(text string) []string
}

// Stemmer reduces a token to its root form (spec 11 §9.3 step 5). The no-op default
// keeps exact-match precision; the Porter stemmer is opt-in.
type Stemmer interface {
	Stem(token string) string
}

// noStemmer returns tokens unchanged.
type noStemmer struct{}

func (noStemmer) Stem(t string) string { return t }

// StandardTokenizer is the default tokenizer of spec 11 §9.3: Unicode word
// segmentation, lowercase folding, punctuation stripping, with optional stopword
// removal and stemming (both off by default).
type StandardTokenizer struct {
	Lowercase     bool
	RemoveStop    bool
	Stopwords     map[string]struct{}
	Stem          Stemmer
	MinTokenRunes int // drop tokens shorter than this after stripping (0 = keep all)
}

// NewStandardTokenizer returns the default tokenizer: lowercase on, stopwords and
// stemming off (spec 11 §9.3 defaults).
func NewStandardTokenizer() *StandardTokenizer {
	return &StandardTokenizer{Lowercase: true, Stem: noStemmer{}}
}

// Tokenize implements the default pipeline. Word segmentation splits on any rune that
// is neither a letter nor a digit (a practical UAX #29 approximation that keeps
// intra-word digits and letters together), then each token is lowercased, its
// leading and trailing punctuation stripped, optionally stopword-filtered, and
// optionally stemmed.
func (t *StandardTokenizer) Tokenize(text string) []string {
	raw := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(raw))
	for _, tok := range raw {
		if t.Lowercase {
			tok = strings.ToLower(tok)
		}
		tok = strings.TrimFunc(tok, unicode.IsPunct)
		if tok == "" {
			continue
		}
		if t.MinTokenRunes > 0 && len([]rune(tok)) < t.MinTokenRunes {
			continue
		}
		if t.RemoveStop {
			set := t.Stopwords
			if set == nil {
				set = englishStopwords
			}
			if _, stop := set[tok]; stop {
				continue
			}
		}
		if t.Stem != nil {
			tok = t.Stem.Stem(tok)
		}
		out = append(out, tok)
	}
	return out
}

// dedup returns the distinct tokens in first-seen order; standard BM25 counts each
// query term once (spec 11 §9.5).
func dedup(tokens []string) []string {
	seen := make(map[string]struct{}, len(tokens))
	out := tokens[:0:0]
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// englishStopwords is a small default English stopword set (spec 11 §9.3 step 4),
// applied only when stopword removal is enabled.
var englishStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"for": {}, "from": {}, "has": {}, "he": {}, "in": {}, "is": {}, "it": {}, "its": {},
	"of": {}, "on": {}, "that": {}, "the": {}, "to": {}, "was": {}, "were": {}, "will": {},
	"with": {}, "this": {}, "these": {}, "those": {}, "or": {}, "but": {}, "not": {},
}
