package verify

import "fmt"

// The metamorphic relations of spec 21 §5.4 state how a query result must change
// under a known input change, without needing to know the exact result. Each one
// is a function over the Collection interface that returns an error describing
// the violation, or nil. They run against any index family by being given the
// real collection; the reference model satisfies them trivially and is the
// control.

// InsertionRelation checks the insertion relation (spec 21 §5.4): a point
// strictly closer to q than the current top-1 must become the new top-1. closer
// is a vector the caller has built to sit nearer q than the present best; newID
// is the id it is upserted under. The caller picks newID outside the live set so
// the insert is unambiguous.
func InsertionRelation(c Collection, q []float32, closer []float32, newID uint64) error {
	if err := c.Upsert(ModelPoint{ID: newID, Vec: closer}); err != nil {
		return fmt.Errorf("insertion relation: upsert: %w", err)
	}
	res, err := c.Query(q, 1, QueryOpts{})
	if err != nil {
		return fmt.Errorf("insertion relation: query: %w", err)
	}
	if len(res) == 0 || res[0].ID != newID {
		return fmt.Errorf("insertion relation: closer point %d did not become top-1 (got %v)", newID, res)
	}
	return nil
}

// DeletionRelation checks the deletion relation (spec 21 §5.4): after deleting a
// point, it must not appear in any subsequent query result, however relevant its
// vector is. It deletes victimID, then queries q at each k in ks and asserts the
// victim is absent.
func DeletionRelation(c Collection, q []float32, victimID uint64, ks []int) error {
	if err := c.Delete(victimID); err != nil {
		return fmt.Errorf("deletion relation: delete: %w", err)
	}
	for _, k := range ks {
		res, err := c.Query(q, k, QueryOpts{})
		if err != nil {
			return fmt.Errorf("deletion relation: query k=%d: %w", k, err)
		}
		for _, r := range res {
			if r.ID == victimID {
				return fmt.Errorf("deletion relation: deleted point %d appeared in top-%d", victimID, k)
			}
		}
	}
	return nil
}

// ReindexRelation checks the reindex relation (spec 21 §5.4): rebuilding the
// index on the same data must not meaningfully reduce recall. It measures mean
// recall over queries before and after calling reindex, against the reference
// flat result, and allows a small slack for build randomness (spec 21 §20.1).
func ReindexRelation(c Collection, ref Collection, queries [][]float32, k int, reindex func() error, slack float64) error {
	before, err := meanRecallAgainst(c, ref, queries, k)
	if err != nil {
		return err
	}
	if err := reindex(); err != nil {
		return fmt.Errorf("reindex relation: reindex: %w", err)
	}
	after, err := meanRecallAgainst(c, ref, queries, k)
	if err != nil {
		return err
	}
	if after < before-slack {
		return fmt.Errorf("reindex relation: recall dropped %.4f -> %.4f (slack %.4f)", before, after, slack)
	}
	return nil
}

// FilterRelation checks the filter relation (spec 21 §5.4): a filtered query
// returns only points that pass the filter, and every returned point appears in
// the unfiltered result widened to k*widen. The first part is precision, the
// second is that the filter restricts rather than reorders.
func FilterRelation(c Collection, q []float32, k, widen int, filter Predicate, passes func(id uint64) bool) error {
	filtered, err := c.Query(q, k, QueryOpts{Filter: filter})
	if err != nil {
		return fmt.Errorf("filter relation: filtered query: %w", err)
	}
	for _, r := range filtered {
		if !passes(r.ID) {
			return fmt.Errorf("filter relation: point %d in result does not pass the filter", r.ID)
		}
	}
	wide, err := c.Query(q, k*widen, QueryOpts{})
	if err != nil {
		return fmt.Errorf("filter relation: wide query: %w", err)
	}
	in := make(map[uint64]struct{}, len(wide))
	for _, r := range wide {
		in[r.ID] = struct{}{}
	}
	for _, r := range filtered {
		if _, ok := in[r.ID]; !ok {
			return fmt.Errorf("filter relation: filtered point %d not in unfiltered top-%d", r.ID, k*widen)
		}
	}
	return nil
}

func meanRecallAgainst(c, ref Collection, queries [][]float32, k int) (float64, error) {
	if len(queries) == 0 {
		return 1.0, nil
	}
	var sum float64
	for _, q := range queries {
		got, err := c.Query(q, k, QueryOpts{})
		if err != nil {
			return 0, err
		}
		truth, err := ref.Query(q, k, QueryOpts{})
		if err != nil {
			return 0, err
		}
		sum += MeasureRecall(IDs(got), IDs(truth))
	}
	return sum / float64(len(queries)), nil
}
