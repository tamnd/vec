package server

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tamnd/vec"
)

// The neutral data types below are the server's own view of a request and a
// result. REST decodes JSON into them, the gRPC layer fills them from proto3
// messages, and the engine operations in this file consume them. Keeping them
// independent of both wire formats means the engine logic is written once.

// collectionConfig describes a collection to create (spec 16 §3).
type collectionConfig struct {
	Dim         int
	Metric      string
	IndexType   string
	IndexParams map[string]string
	ColumnTypes map[string]string
}

// collectionInfo is a snapshot of a collection returned to a client.
type collectionInfo struct {
	Name       string
	Config     collectionConfig
	Count      int64
	IndexState string
}

// point is one row to upsert or one row read back.
type point struct {
	ID      uint64
	Vector  []float32
	Payload map[string]any
}

// scoredPoint is one query result, ordered by distance.
type scoredPoint struct {
	ID      uint64
	Score   float64
	Rank    uint64
	Vector  []float32
	Payload map[string]any
}

// queryRequest is one ANN search.
type queryRequest struct {
	Vector      []float32
	TopK        int
	Filter      *filterNode
	Params      map[string]string
	WithVectors bool
	WithPayload bool
	IndexName   string
}

// queryResult is the outcome of a search.
type queryResult struct {
	Results      []scoredPoint
	SearchTimeMS float64
	Strategy     string
}

// vecColumn is the conventional name of the vector column in a collection the
// server creates. Queries find the column by type, so a different name still
// works, but new collections use this one.
const vecColumn = "embedding"

// createCollection lowers a config onto the typed API and builds the index if one
// was requested (spec 16 §3.2).
func (s *Server) createCollection(ctx context.Context, name string, cfg collectionConfig) (collectionInfo, error) {
	metric, err := parseMetric(cfg.Metric)
	if err != nil {
		return collectionInfo{}, err
	}
	cols := []vec.ColumnDef{{
		Name:   vecColumn,
		Type:   vec.TypeVector,
		Dim:    cfg.Dim,
		Metric: metric,
	}}
	names := make([]string, 0, len(cfg.ColumnTypes))
	for col := range cfg.ColumnTypes {
		names = append(names, col)
	}
	sort.Strings(names)
	for _, col := range names {
		ct, err := parseColumnType(cfg.ColumnTypes[col])
		if err != nil {
			return collectionInfo{}, err
		}
		cols = append(cols, vec.ColumnDef{Name: col, Type: ct})
	}
	schema := vec.CollectionSchema{Name: name, Columns: cols}
	if err := s.db.CreateCollection(ctx, schema); err != nil {
		return collectionInfo{}, err
	}
	if it := strings.ToLower(cfg.IndexType); it != "" && it != "flat" {
		spec := vec.IndexSpec{
			Name:   name + "_idx",
			Column: vecColumn,
			Type:   parseIndexType(it),
			Params: indexParams(cfg.IndexParams),
		}
		if err := s.db.CreateIndex(ctx, name, spec); err != nil {
			return collectionInfo{}, err
		}
	}
	return s.getCollection(ctx, name)
}

// getCollection reads one collection's metadata.
func (s *Server) getCollection(ctx context.Context, name string) (collectionInfo, error) {
	info, err := s.db.GetCollection(ctx, name)
	if err != nil {
		return collectionInfo{}, err
	}
	return s.describeCollection(ctx, info), nil
}

// listCollections reads every collection's metadata.
func (s *Server) listCollections(ctx context.Context) ([]collectionInfo, error) {
	infos, err := s.db.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]collectionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, s.describeCollection(ctx, info))
	}
	return out, nil
}

// describeCollection projects a facade CollectionInfo into the server view.
func (s *Server) describeCollection(ctx context.Context, info vec.CollectionInfo) collectionInfo {
	ci := collectionInfo{
		Name:       info.Name,
		Count:      info.PointCount,
		IndexState: "ready",
		Config: collectionConfig{
			Metric:      "l2",
			IndexType:   "flat",
			ColumnTypes: map[string]string{},
		},
	}
	for _, c := range info.Columns {
		if c.Type == vec.TypeVector {
			ci.Config.Dim = c.Dim
			ci.Config.Metric = metricName(c.Metric)
			continue
		}
		ci.Config.ColumnTypes[c.Name] = c.Type.String()
	}
	if idxs, err := s.db.ListIndexes(ctx, info.Name); err == nil && len(idxs) > 0 {
		ci.Config.IndexType = idxs[0].Type.String()
	}
	return ci
}

// dropCollection removes a collection and its index.
func (s *Server) dropCollection(ctx context.Context, name string) error {
	return s.db.DropCollection(ctx, name)
}

// upsertPoints writes a batch of points through the writer pipeline so all writes
// share the single-writer commit path (spec 16 §9.1).
func (s *Server) upsertPoints(ctx context.Context, name string, pts []point) (int, error) {
	coll, err := s.db.Collection(name)
	if err != nil {
		return 0, err
	}
	col, err := s.vectorColumn(ctx, coll)
	if err != nil {
		return 0, err
	}
	batch := make([]vec.Point, len(pts))
	for i, p := range pts {
		meta, err := toMeta(p.Payload)
		if err != nil {
			return 0, err
		}
		batch[i] = vec.Point{
			ID:      vec.IntID(p.ID),
			Vectors: map[string]vec.AnyVector{col: {Dense: vec.FromSlice32(p.Vector)}},
			Meta:    meta,
		}
	}
	err = s.write(ctx, func() error {
		_, e := coll.UpsertBatch(ctx, batch)
		return e
	})
	if err != nil {
		return 0, err
	}
	return len(batch), nil
}

// deletePoints removes points by id through the writer pipeline.
func (s *Server) deletePoints(ctx context.Context, name string, ids []uint64) (int, error) {
	coll, err := s.db.Collection(name)
	if err != nil {
		return 0, err
	}
	pids := make([]vec.PointID, len(ids))
	for i, id := range ids {
		pids[i] = vec.IntID(id)
	}
	err = s.write(ctx, func() error {
		return coll.DeleteBatch(ctx, pids)
	})
	if err != nil {
		return 0, err
	}
	return len(pids), nil
}

// getPoints reads points by id.
func (s *Server) getPoints(ctx context.Context, name string, ids []uint64, withVectors, withPayload bool) ([]point, error) {
	coll, err := s.db.Collection(name)
	if err != nil {
		return nil, err
	}
	col, err := s.vectorColumn(ctx, coll)
	if err != nil {
		return nil, err
	}
	txn, err := s.db.Begin(ctx, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = txn.Rollback() }()
	pids := make([]vec.PointID, len(ids))
	for i, id := range ids {
		pids[i] = vec.IntID(id)
	}
	rows, err := coll.GetBatch(txn, pids)
	if err != nil {
		return nil, err
	}
	out := make([]point, 0, len(rows))
	for _, p := range rows {
		op := point{ID: p.ID.N}
		if withVectors {
			if av, ok := p.Vectors[col]; ok {
				op.Vector = av.Dense.ToSlice32()
			}
		}
		if withPayload {
			op.Payload = fromMeta(p.Meta)
		}
		out = append(out, op)
	}
	return out, nil
}

// queryPoints runs one ANN search and returns the ranked results.
func (s *Server) queryPoints(ctx context.Context, name string, req queryRequest) (queryResult, error) {
	coll, err := s.db.Collection(name)
	if err != nil {
		return queryResult{}, err
	}
	col, err := s.vectorColumn(ctx, coll)
	if err != nil {
		return queryResult{}, err
	}
	k := req.TopK
	if k <= 0 {
		k = 10
	}
	qb := coll.Query(col, vec.FromSlice32(req.Vector)).K(k)
	if req.Filter != nil {
		expr, args, err := compileFilter(req.Filter)
		if err != nil {
			return queryResult{}, err
		}
		if expr != "" {
			qb = qb.Filter(expr, args...)
		}
	}
	if ef := paramInt(req.Params, "ef_search"); ef > 0 {
		qb = qb.Ef(ef)
	}
	if np := paramInt(req.Params, "nprobe"); np > 0 {
		qb = qb.Nprobe(np)
	}
	if req.IndexName != "" {
		qb = qb.WithIndex(req.IndexName)
	}
	if req.WithVectors {
		qb = qb.WithVectors(col)
	}
	start := s.clock()
	results, err := qb.All(ctx)
	if err != nil {
		return queryResult{}, err
	}
	out := queryResult{
		Results:      make([]scoredPoint, len(results)),
		SearchTimeMS: float64(s.clock()-start) / 1e6,
		Strategy:     "ann",
	}
	for i, r := range results {
		sp := scoredPoint{ID: r.ID.N, Score: float64(r.Distance), Rank: uint64(i + 1)}
		if req.WithVectors {
			if v, ok := r.Vector(col); ok {
				sp.Vector = v.ToSlice32()
			}
		}
		if req.WithPayload {
			sp.Payload = fromMeta(r.Point.Meta)
		}
		out.Results[i] = sp
	}
	return out, nil
}

// vectorColumn returns the name of the collection's vector column.
func (s *Server) vectorColumn(ctx context.Context, coll *vec.Collection) (string, error) {
	schema, err := coll.Schema(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range schema.Columns {
		if c.Type == vec.TypeVector {
			return c.Name, nil
		}
	}
	return "", fmt.Errorf("collection %q has no vector column", coll.Name())
}

// compileFilter turns a filter tree into a WHERE expression with ? placeholders
// and the matching argument slice (spec 16 §3 filter DSL).
func compileFilter(n *filterNode) (string, []any, error) {
	if n == nil {
		return "", nil, nil
	}
	var args []any
	expr, err := filterExpr(n, &args)
	return expr, args, err
}

func filterExpr(n *filterNode, args *[]any) (string, error) {
	// A combinator node holds child clauses; a leaf node holds a field test.
	if len(n.Must) > 0 || len(n.Should) > 0 || len(n.MustNot) > 0 {
		var parts []string
		if s, err := joinClauses(n.Must, " AND ", args); err != nil {
			return "", err
		} else if s != "" {
			parts = append(parts, s)
		}
		if s, err := joinClauses(n.Should, " OR ", args); err != nil {
			return "", err
		} else if s != "" {
			parts = append(parts, s)
		}
		for _, c := range n.MustNot {
			e, err := filterExpr(c, args)
			if err != nil {
				return "", err
			}
			parts = append(parts, "NOT ("+e+")")
		}
		return strings.Join(parts, " AND "), nil
	}
	return leafExpr(n, args)
}

func joinClauses(clauses []*filterNode, sep string, args *[]any) (string, error) {
	if len(clauses) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(clauses))
	for _, c := range clauses {
		e, err := filterExpr(c, args)
		if err != nil {
			return "", err
		}
		parts = append(parts, "("+e+")")
	}
	return strings.Join(parts, sep), nil
}

func leafExpr(n *filterNode, args *[]any) (string, error) {
	if n.Field == "" {
		return "", fmt.Errorf("filter leaf has no field")
	}
	switch {
	case n.Range != nil:
		var parts []string
		r := n.Range
		if r.Gt != nil {
			parts = append(parts, n.Field+" > ?")
			*args = append(*args, *r.Gt)
		}
		if r.Gte != nil {
			parts = append(parts, n.Field+" >= ?")
			*args = append(*args, *r.Gte)
		}
		if r.Lt != nil {
			parts = append(parts, n.Field+" < ?")
			*args = append(*args, *r.Lt)
		}
		if r.Lte != nil {
			parts = append(parts, n.Field+" <= ?")
			*args = append(*args, *r.Lte)
		}
		return strings.Join(parts, " AND "), nil
	case n.Match != nil:
		*args = append(*args, n.Match.Value)
		return n.Field + " = ?", nil
	case n.IsNull != nil:
		if *n.IsNull {
			return n.Field + " IS NULL", nil
		}
		return n.Field + " IS NOT NULL", nil
	default:
		return "", fmt.Errorf("filter leaf on %q has no test", n.Field)
	}
}

// filterNode mirrors the JSON and proto filter DSL (spec 16 §3).
type filterNode struct {
	Must    []*filterNode
	Should  []*filterNode
	MustNot []*filterNode

	Field  string
	Range  *rangeTest
	Match  *matchTest
	IsNull *bool
}

type rangeTest struct {
	Gt, Gte, Lt, Lte *float64
}

type matchTest struct {
	Value any
}

// paramInt reads an integer query parameter, returning 0 when absent or unparseable.
func paramInt(params map[string]string, key string) int {
	if params == nil {
		return 0
	}
	n, err := strconv.Atoi(params[key])
	if err != nil {
		return 0
	}
	return n
}
