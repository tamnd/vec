package hybrid

import (
	"context"
	"math"
	"testing"

	"github.com/tamnd/vector/distance"
	"github.com/tamnd/vector/index"
)

func TestStandardTokenizer(t *testing.T) {
	tok := NewStandardTokenizer()
	got := tok.Tokenize("The Quick, brown FOX! 42 jumps.")
	want := []string{"the", "quick", "brown", "fox", "42", "jumps"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestPorterStemmer(t *testing.T) {
	s := PorterStemmer{}
	cases := map[string]string{
		"running":    "run",
		"cats":       "cat",
		"ponies":     "poni",
		"caresses":   "caress",
		"relational": "relat",
		"happy":      "happi",
		"motoring":   "motor",
		"sing":       "sing",
	}
	for in, want := range cases {
		if got := s.Stem(in); got != want {
			t.Errorf("Stem(%q) = %q want %q", in, got, want)
		}
	}
}

func TestBM25RanksByRelevance(t *testing.T) {
	idx := NewBM25Index(nil, DefaultBM25Params())
	idx.AddDoc(1, "the cat sat on the mat")
	idx.AddDoc(2, "the dog chased the cat up a tree")
	idx.AddDoc(3, "quantum mechanics and general relativity")
	idx.AddDoc(4, "a cat and a dog and another cat") // cat appears twice

	res := idx.Search("cat", 10, nil)
	if len(res) != 3 {
		t.Fatalf("expected 3 docs containing cat, got %d", len(res))
	}
	// Doc 4 has the highest term frequency for "cat" relative to its length, so it
	// should outrank the single-occurrence docs.
	if res[0].Pos != 4 {
		t.Fatalf("expected doc 4 ranked first, got %d", res[0].Pos)
	}
	// Doc 3 has no "cat" and must not appear.
	for _, r := range res {
		if r.Pos == 3 {
			t.Fatal("doc 3 should not match cat")
		}
	}
}

func TestBM25IDFDownweightsCommonTerms(t *testing.T) {
	idx := NewBM25Index(nil, DefaultBM25Params())
	for i := uint32(1); i <= 10; i++ {
		idx.AddDoc(i, "common term appears everywhere")
	}
	idx.AddDoc(11, "common rare unicorn")
	res := idx.Search("common rare", 20, nil)
	if res[0].Pos != 11 {
		t.Fatalf("doc with the rare term should rank first, got %d", res[0].Pos)
	}
}

func TestBM25DeleteAndFilter(t *testing.T) {
	idx := NewBM25Index(nil, DefaultBM25Params())
	idx.AddDoc(1, "alpha beta")
	idx.AddDoc(2, "alpha gamma")
	idx.AddDoc(3, "alpha delta")
	idx.Remove(2)
	res := idx.Search("alpha", 10, nil)
	for _, r := range res {
		if r.Pos == 2 {
			t.Fatal("deleted doc 2 should not appear")
		}
	}
	if idx.DocCount() != 2 {
		t.Fatalf("doc count after delete = %d want 2", idx.DocCount())
	}

	only1 := bitmapOf(1)
	res = idx.Search("alpha", 10, only1)
	if len(res) != 1 || res[0].Pos != 1 {
		t.Fatalf("filtered search should return only doc 1, got %v", res)
	}
}

func TestBM25FieldWeights(t *testing.T) {
	idx := NewBM25Index(nil, DefaultBM25Params())
	idx.SetFieldWeights([]float64{3.0, 1.0}) // title weighted 3x body
	// Doc 1 has the term in the title; doc 2 has it in the body.
	idx.AddDocFields(1, []string{"machine learning", "an introduction"})
	idx.AddDocFields(2, []string{"an introduction", "machine learning"})
	res := idx.Search("machine", 10, nil)
	if len(res) != 2 {
		t.Fatalf("both docs should match, got %d", len(res))
	}
	if res[0].Pos != 1 {
		t.Fatalf("title-weighted doc 1 should rank first, got %d", res[0].Pos)
	}
}

func TestRRFuse(t *testing.T) {
	dense := []ScoredPos{{Pos: 10, Score: 0.9}, {Pos: 20, Score: 0.8}, {Pos: 30, Score: 0.7}}
	sparse := []ScoredPos{{Pos: 20, Score: 5.0}, {Pos: 40, Score: 4.0}, {Pos: 10, Score: 3.0}}
	fused := RRFuse([][]ScoredPos{dense, sparse}, DefaultRRFK)
	// Position 20 is rank 2 in dense and rank 1 in sparse; position 10 is rank 1 and
	// rank 3. RRF: 20 -> 1/62 + 1/61, 10 -> 1/61 + 1/63. 20 should edge out 10.
	if fused[0].Pos != 20 {
		t.Fatalf("expected pos 20 fused first, got %d (%v)", fused[0].Pos, fused)
	}
	// Every input position appears exactly once.
	seen := map[uint32]int{}
	for _, sp := range fused {
		seen[sp.Pos]++
	}
	for _, p := range []uint32{10, 20, 30, 40} {
		if seen[p] != 1 {
			t.Fatalf("pos %d appeared %d times", p, seen[p])
		}
	}
}

func TestWeightedRRFFavorsList(t *testing.T) {
	a := []ScoredPos{{Pos: 1, Score: 1}, {Pos: 2, Score: 1}}
	b := []ScoredPos{{Pos: 2, Score: 1}, {Pos: 1, Score: 1}}
	// Weight list b heavily: position 2 (rank 1 in b) should win.
	fused := WeightedRRFuse([][]ScoredPos{a, b}, []float64{0.1, 5.0}, DefaultRRFK)
	if fused[0].Pos != 2 {
		t.Fatalf("heavily-weighted list should put pos 2 first, got %d", fused[0].Pos)
	}
}

func TestFusionMethods(t *testing.T) {
	a := []ScoredPos{{Pos: 1, Score: 100}, {Pos: 2, Score: 1}}
	b := []ScoredPos{{Pos: 2, Score: 10}, {Pos: 1, Score: 9}}
	for _, m := range []FusionMethod{FusionRRF, FusionMinMax, FusionZScore} {
		fused := Fuse(m, [][]ScoredPos{a, b}, nil, DefaultRRFK)
		if len(fused) != 2 {
			t.Fatalf("method %d: expected 2 results, got %d", m, len(fused))
		}
	}
}

func TestSparseSearch(t *testing.T) {
	idx := NewSparseIndex()
	idx.AddDoc(1, []SparsePair{{Index: 5, Value: 1.0}, {Index: 9, Value: 2.0}})
	idx.AddDoc(2, []SparsePair{{Index: 9, Value: 0.5}, {Index: 12, Value: 3.0}})
	idx.AddDoc(3, []SparsePair{{Index: 1, Value: 4.0}})
	// Query overlaps doc 1 on dim 9 (2*2=4) and dim 5 (1*1=1) => 5; doc 2 on dim 9
	// (2*0.5=1).
	q := []SparsePair{{Index: 5, Value: 1.0}, {Index: 9, Value: 2.0}}
	res := idx.Search(q, 10, nil)
	if len(res) != 2 {
		t.Fatalf("expected 2 matching docs, got %d", len(res))
	}
	if res[0].Pos != 1 {
		t.Fatalf("doc 1 should score highest, got %d", res[0].Pos)
	}
	if math.Abs(res[0].Score-5.0) > 1e-6 {
		t.Fatalf("doc 1 score = %v want 5.0", res[0].Score)
	}
}

func TestMaxSimSearch(t *testing.T) {
	// 2-d token vectors; two query tokens. Build docs whose tokens align with the
	// query to varying degrees.
	dim := 2
	mv := NewMultiVecIndex(dim, index.NewFlat(dim, distance.Dot))
	// Doc 1: tokens match both query directions strongly.
	must(t, mv.AddDoc(1, [][]float32{{1, 0}, {0, 1}}))
	// Doc 2: tokens match only the first query direction.
	must(t, mv.AddDoc(2, [][]float32{{1, 0}, {1, 0}}))
	// Doc 3: orthogonal-ish, weak match.
	must(t, mv.AddDoc(3, [][]float32{{-1, 0}, {0, -1}}))

	query := [][]float32{{1, 0}, {0, 1}}
	res, err := mv.Search(context.Background(), query, 3, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("expected candidates")
	}
	if res[0].Pos != 1 {
		t.Fatalf("doc 1 should have the highest MaxSim, got %d (%v)", res[0].Pos, res)
	}
	// Exact MaxSim for doc 1 = max(q0.d) + max(q1.d) = 1 + 1 = 2.
	if math.Abs(res[0].Score-2.0) > 1e-6 {
		t.Fatalf("doc 1 MaxSim = %v want 2.0", res[0].Score)
	}
}

// bitmapOf is a tiny test Bitmap over a fixed position set.
type setBitmap map[uint32]struct{}

func (s setBitmap) Contains(p uint32) bool { _, ok := s[p]; return ok }

func bitmapOf(ps ...uint32) Bitmap {
	s := make(setBitmap, len(ps))
	for _, p := range ps {
		s[p] = struct{}{}
	}
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
