package verify

import (
	"fmt"
	"math/rand"

	"github.com/tamnd/vec/distance"
)

// OpKind is the kind of a generated collection operation (spec 21 §5.2).
type OpKind int

const (
	OpUpsert OpKind = iota
	OpDelete
	OpGet
	OpQuery
)

// String renders an OpKind for diagnostics.
func (k OpKind) String() string {
	switch k {
	case OpUpsert:
		return "upsert"
	case OpDelete:
		return "delete"
	case OpGet:
		return "get"
	case OpQuery:
		return "query"
	default:
		return "op?"
	}
}

// Op is one operation in a generated sequence (spec 21 §5.2). Only the fields
// that the Kind uses are set: Upsert uses Point, Delete and Get use ID, Query
// uses Vec, K, and Filter.
type Op struct {
	Kind   OpKind
	Point  ModelPoint
	ID     uint64
	Vec    []float32
	K      int
	Filter Predicate
}

// GenSchema parameterizes the operation generator (spec 21 §5.2): the vector
// dimension, the id space the operations draw from, and the top-k a query asks
// for. A small id space relative to the operation count makes deletes and
// re-upserts of the same id likely, which is where the interesting divergences
// hide.
type GenSchema struct {
	Dim     int
	MaxID   uint64
	QueryK  int
	WithDel bool
}

// GenerateOps produces a reproducible, realistic operation sequence (spec 21
// §5.2). The same seed yields the same sequence, so a seed that finds a bug
// becomes a permanent regression seed. The mix is upsert-heavy with some
// deletes, gets, and queries, matching a real write/read ratio.
func GenerateOps(rng *rand.Rand, n int, schema GenSchema) []Op {
	if schema.MaxID == 0 {
		schema.MaxID = 64
	}
	if schema.QueryK == 0 {
		schema.QueryK = 10
	}
	ops := make([]Op, 0, n)
	for i := 0; i < n; i++ {
		roll := rng.Float64()
		id := uint64(rng.Int63n(int64(schema.MaxID))) + 1
		switch {
		case roll < 0.55:
			ops = append(ops, Op{Kind: OpUpsert, Point: ModelPoint{ID: id, Vec: randVec(rng, schema.Dim)}})
		case roll < 0.70 && schema.WithDel:
			ops = append(ops, Op{Kind: OpDelete, ID: id})
		case roll < 0.85:
			ops = append(ops, Op{Kind: OpGet, ID: id})
		default:
			ops = append(ops, Op{Kind: OpQuery, Vec: randVec(rng, schema.Dim), K: schema.QueryK})
		}
	}
	return ops
}

func randVec(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rng.Float32()*2 - 1
	}
	return v
}

// Divergence is one place where the real collection and the reference model
// disagree while replaying an operation (spec 21 §5.3).
type Divergence struct {
	Index  int
	Op     Op
	Reason string
}

func (d Divergence) String() string {
	return fmt.Sprintf("op %d (%s): %s", d.Index, d.Op.Kind, d.Reason)
}

// RecallFunc gives the minimum acceptable recall for a query op (spec 21 §5.3).
// It is supplied by the caller because the threshold depends on the index type
// and the search effort, which the verify package does not know. A flat index
// passes 1.0; an ANN index passes its per-regime threshold from spec §2.3.
type RecallFunc func(op Op) float64

// RunConformance replays ops against the real collection and the reference model
// and collects every divergence (spec 21 §5.3). Upsert and Delete must agree on
// whether they errored; Get must agree on presence and on the stored vector;
// Query must clear the recall threshold against the reference flat result. It
// returns the divergences rather than failing a test directly, so the caller
// owns reporting and a fuzz driver can shrink on the count.
func RunConformance(real, ref Collection, ops []Op, recall RecallFunc) []Divergence {
	var out []Divergence
	for i, op := range ops {
		switch op.Kind {
		case OpUpsert:
			errReal := real.Upsert(op.Point)
			errRef := ref.Upsert(op.Point)
			if (errReal != nil) != (errRef != nil) {
				out = append(out, Divergence{i, op, fmt.Sprintf("upsert error mismatch: real=%v ref=%v", errReal, errRef)})
			}
		case OpDelete:
			errReal := real.Delete(op.ID)
			errRef := ref.Delete(op.ID)
			if (errReal != nil) != (errRef != nil) {
				out = append(out, Divergence{i, op, fmt.Sprintf("delete error mismatch: real=%v ref=%v", errReal, errRef)})
			}
		case OpGet:
			gotReal, okReal, errReal := real.Get(op.ID)
			gotRef, okRef, errRef := ref.Get(op.ID)
			if errReal != nil || errRef != nil {
				if (errReal != nil) != (errRef != nil) {
					out = append(out, Divergence{i, op, fmt.Sprintf("get error mismatch: real=%v ref=%v", errReal, errRef)})
				}
				continue
			}
			if okReal != okRef {
				out = append(out, Divergence{i, op, fmt.Sprintf("get presence mismatch: real=%v ref=%v", okReal, okRef)})
				continue
			}
			if okReal && !vecEqual(gotReal.Vec, gotRef.Vec) {
				out = append(out, Divergence{i, op, fmt.Sprintf("get vector mismatch for id %d", op.ID)})
			}
		case OpQuery:
			resReal, errReal := real.Query(op.Vec, op.K, QueryOpts{Filter: op.Filter})
			resRef, errRef := ref.Query(op.Vec, op.K, QueryOpts{Filter: op.Filter})
			if errReal != nil || errRef != nil {
				out = append(out, Divergence{i, op, fmt.Sprintf("query error: real=%v ref=%v", errReal, errRef)})
				continue
			}
			r := MeasureRecall(IDs(resReal), IDs(resRef))
			min := 1.0
			if recall != nil {
				min = recall(op)
			}
			if r < min-1e-9 {
				out = append(out, Divergence{i, op, fmt.Sprintf("recall %.4f below threshold %.4f", r, min)})
			}
		}
	}
	return out
}

func vecEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FlatRecall is the recall threshold for an index that searches exactly: every
// query must reproduce the reference result, so the threshold is 1.0. It is the
// default RecallFunc for conformance against a flat or reference collection.
func FlatRecall(metric distance.Metric) RecallFunc {
	_ = metric
	return func(Op) float64 { return 1.0 }
}
