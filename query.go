package vec

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/vec/query"
	"github.com/tamnd/vec/vectorsql"
)

// QueryBuilder builds and executes ANN queries with a fluent API (spec 14 §5).
// It is created by Collection.Query and is not goroutine-safe; build and execute
// it from one goroutine.
type QueryBuilder struct {
	coll   *Collection
	column string
	vector Vector
	sparse *SparseVector
	multi  MultiVector

	k          int
	filterExpr string
	filterArgs []any
	ef         int
	nprobe     int
	rerank     int
	project    []string
	withVecs   []string
	orderBy    string
	orderDesc  bool
	indexName  string
	fetchSize  int
	allowFlat  bool

	bm25Field string
	bm25Query string
	rrfK      float64

	unsupported error
}

// K sets the number of results to return.
func (qb *QueryBuilder) K(k int) *QueryBuilder { qb.k = k; return qb }

// Filter adds a VectorSQL WHERE clause with positional ? arguments.
func (qb *QueryBuilder) Filter(expr string, args ...any) *QueryBuilder {
	qb.filterExpr = expr
	qb.filterArgs = args
	return qb
}

// Ef sets the HNSW ef_search beam width for this query.
func (qb *QueryBuilder) Ef(ef int) *QueryBuilder { qb.ef = ef; return qb }

// Nprobe sets the IVF probe count for this query.
func (qb *QueryBuilder) Nprobe(n int) *QueryBuilder { qb.nprobe = n; return qb }

// Rerank requests an exact rerank over r widened candidates.
func (qb *QueryBuilder) Rerank(r int) *QueryBuilder { qb.rerank = r; return qb }

// Select restricts the projected metadata columns.
func (qb *QueryBuilder) Select(columns ...string) *QueryBuilder { qb.project = columns; return qb }

// WithVectors requests the stored vectors of the named columns in each Result.
func (qb *QueryBuilder) WithVectors(columns ...string) *QueryBuilder {
	qb.withVecs = columns
	return qb
}

// OrderBy adds a secondary ordering by a metadata column.
func (qb *QueryBuilder) OrderBy(column string, desc bool) *QueryBuilder {
	qb.orderBy = column
	qb.orderDesc = desc
	return qb
}

// WithIndex pins a named index for this query.
func (qb *QueryBuilder) WithIndex(indexName string) *QueryBuilder {
	qb.indexName = indexName
	return qb
}

// WithFetchSize sets the streaming fetch size for the cursor.
func (qb *QueryBuilder) WithFetchSize(n int) *QueryBuilder { qb.fetchSize = n; return qb }

// BM25 adds a lexical BM25 scoring term over a text field (spec 14 §5, hybrid).
func (qb *QueryBuilder) BM25(field, queryText string) *QueryBuilder {
	qb.bm25Field = field
	qb.bm25Query = queryText
	return qb
}

// RRF sets the reciprocal-rank-fusion constant for hybrid queries.
func (qb *QueryBuilder) RRF(k float64) *QueryBuilder { qb.rrfK = k; return qb }

// WithScoreMode is reserved for weighted hybrid fusion (spec 14 §5).
func (qb *QueryBuilder) WithScoreMode(annWeight, bm25Weight float64) *QueryBuilder { return qb }

// Exec executes the query and returns a streaming cursor (spec 14 §5).
func (qb *QueryBuilder) Exec(ctx context.Context) (*Rows, error) {
	return qb.execWith(ctx, nil)
}

// ExecTxn executes the query inside txn (spec 14 §5).
func (qb *QueryBuilder) ExecTxn(ctx context.Context, txn *Txn) (*Rows, error) {
	return qb.execWith(ctx, txn)
}

// execWith plans and runs the query, returning a materialized cursor.
func (qb *QueryBuilder) execWith(ctx context.Context, txn *Txn) (*Rows, error) {
	if qb.unsupported != nil {
		return nil, qb.unsupported
	}
	if qb.bm25Field != "" {
		return nil, errUnsupported // hybrid fusion lands with the hybrid wiring (spec 17)
	}
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(err)
	}
	cs, err := qb.coll.db.state(qb.coll.name)
	if err != nil {
		return nil, err
	}
	qcoll := qb.coll.db.queryCollection(cs)
	if len(qb.vector) != qcoll.Dims {
		return nil, &DimError{Column: qb.column, Expected: qcoll.Dims, Got: len(qb.vector)}
	}

	bq, err := qb.boundQuery(cs)
	if err != nil {
		return nil, err
	}

	planner := query.NewPlanner(qcoll, 0)
	plan, err := planner.Plan(bq)
	if err != nil {
		return nil, mapPlanErr(err)
	}
	exec := query.NewExecutor(qcoll)
	rs, err := exec.Execute(ctx, plan, qb.vector)
	if err != nil {
		return nil, mapPlanErr(err)
	}
	return newRows(qb, cs, rs), nil
}

// boundQuery builds the planner's BoundQuery, lowering any WHERE clause to a
// storage predicate through the VectorSQL binder.
func (qb *QueryBuilder) boundQuery(cs *collState) (query.BoundQuery, error) {
	bs, err := qb.bindSelect(cs)
	if err != nil {
		return query.BoundQuery{}, err
	}
	bq := bs.BoundQuery
	bq.Vector = qb.vector // the literal in the bound SQL is a placeholder; use the real vector
	bq.K = qb.k
	bq.IncludeDistance = true
	bq.AllowFlat = qb.allowFlat
	bq.Selectivity = -1
	if qb.ef > 0 {
		bq.EfSearch = qb.ef
	}
	if qb.nprobe > 0 {
		bq.NProbe = qb.nprobe
	}
	if qb.rerank > 0 {
		bq.RerankR = qb.rerank
	}
	return bq, nil
}

// bindSelect assembles a kNN SELECT over the collection and binds it, which lowers
// the WHERE clause to a storage predicate and resolves the projection.
func (qb *QueryBuilder) bindSelect(cs *collState) (*vectorsql.BoundSelect, error) {
	dims := int(cs.cc.StorageDef().Dims)
	proj := "*"
	if len(qb.project) > 0 {
		proj = strings.Join(qb.project, ", ")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "SELECT %s FROM %s", proj, qb.coll.name)
	if qb.filterExpr != "" {
		where, err := buildWhere(qb.filterExpr, qb.filterArgs)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&sb, " WHERE %s", where)
	}
	fmt.Fprintf(&sb, " ORDER BY %s <-> '%s' LIMIT %d", qb.column, zeroLiteral(dims), qb.k)

	stmt, err := vectorsql.Parse(sb.String())
	if err != nil {
		return nil, mapSQLErr(err)
	}
	sel, ok := stmt.(*vectorsql.SelectStmt)
	if !ok {
		return nil, &SchemaError{Reason: "query did not parse as a SELECT"}
	}
	bs, err := vectorsql.BindSelect(sel, cs.cc)
	if err != nil {
		return nil, mapSQLErr(err)
	}
	return bs, nil
}

// All executes the query and returns every result (spec 14 §5).
func (qb *QueryBuilder) All(ctx context.Context) ([]Result, error) {
	rows, err := qb.Exec(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Result
	for rows.Next() {
		out = append(out, rows.Result())
	}
	return out, rows.Err()
}

// First executes the query and returns the single nearest result (spec 14 §5).
func (qb *QueryBuilder) First(ctx context.Context) (Result, error) {
	saved := qb.k
	qb.k = 1
	defer func() { qb.k = saved }()
	rows, err := qb.Exec(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Result{}, err
		}
		return Result{}, ErrNotFound
	}
	return rows.Result(), nil
}

// Explain returns the query plan without executing it (spec 14 §12.5).
func (qb *QueryBuilder) Explain(ctx context.Context) (string, error) {
	cs, err := qb.coll.db.state(qb.coll.name)
	if err != nil {
		return "", err
	}
	qcoll := qb.coll.db.queryCollection(cs)
	bq, err := qb.boundQuery(cs)
	if err != nil {
		return "", err
	}
	plan, err := query.NewPlanner(qcoll, 0).Plan(bq)
	if err != nil {
		return "", mapPlanErr(err)
	}
	return fmt.Sprintf("path=%v filter=%v k=%d ef=%d nprobe=%d rerank=%v est_recall=%.3f est_cost=%.0f",
		plan.Path, plan.Filter, plan.K, plan.EfSearch, plan.NProbe, plan.Rerank, plan.EstRecall, plan.EstCost), nil
}

// Profile executes the query and returns a profile report (spec 14 §12.5). The
// phase-level breakdown lands with the observability subsystem (spec 18).
func (qb *QueryBuilder) Profile(ctx context.Context) (*ProfileResult, error) {
	return nil, errUnsupported
}

// Rows is a streaming result cursor (spec 14 §5.6). It is not goroutine-safe.
type Rows struct {
	results []Result
	pos     int
	err     error
}

// newRows materializes the executor result set into a cursor.
func newRows(qb *QueryBuilder, cs *collState, rs query.ResultSet) *Rows {
	out := &Rows{results: make([]Result, 0, len(rs.Rows))}
	for _, row := range rs.Rows {
		r := Result{
			ID:       PointID{N: uint64(row.PointID)},
			Distance: row.Distance,
			Score:    1 - row.Distance,
			columns:  make(map[string]Value, len(row.Meta)),
		}
		for name, v := range row.Meta {
			r.columns[name] = valueFromStorage(v)
		}
		out.results = append(out.results, r)
	}
	return out
}

// Next advances the cursor; it reports whether a row is available.
func (r *Rows) Next() bool {
	if r.err != nil || r.pos >= len(r.results) {
		return false
	}
	r.pos++
	return true
}

// Result returns the current row.
func (r *Rows) Result() Result {
	if r.pos == 0 || r.pos > len(r.results) {
		return Result{}
	}
	return r.results[r.pos-1]
}

// Scan copies the current row's id and distance into dest (spec 14 §5.6).
func (r *Rows) Scan(dest ...any) error { return r.Result().Scan(dest...) }

// Err returns the first error encountered during iteration.
func (r *Rows) Err() error { return r.err }

// Close releases the cursor.
func (r *Rows) Close() error { r.pos = len(r.results); return nil }

// Result is one row from a query (spec 14 §5.6).
type Result struct {
	ID       PointID
	Distance float32
	Score    float32
	Point    Point

	columns map[string]Value
}

// Column returns a projected metadata column value by name.
func (r Result) Column(name string) (Value, bool) {
	v, ok := r.columns[name]
	return v, ok
}

// Vector returns a requested stored vector by column name.
func (r Result) Vector(column string) (Vector, bool) {
	if r.Point.Vectors == nil {
		return nil, false
	}
	av, ok := r.Point.Vectors[column]
	if !ok || av.Dense == nil {
		return nil, false
	}
	return av.Dense, true
}

// DistanceValue returns the distance of the result.
func (r Result) DistanceValue() float32 { return r.Distance }

// Similarity returns a similarity score derived from the distance.
func (r Result) Similarity() float32 { return 1 - r.Distance }

// Scan copies the id and distance of the result into dest. Supported destinations
// are *uint64/*int64 (id) and *float32/*float64 (distance), in that order.
func (r Result) Scan(dest ...any) error {
	for i, d := range dest {
		switch i {
		case 0:
			switch p := d.(type) {
			case *uint64:
				*p = r.ID.N
			case *int64:
				*p = int64(r.ID.N)
			default:
				return fmt.Errorf("vec: Scan dest 0 must be *uint64 or *int64: %w", ErrSchemaViolation)
			}
		case 1:
			switch p := d.(type) {
			case *float32:
				*p = r.Distance
			case *float64:
				*p = float64(r.Distance)
			default:
				return fmt.Errorf("vec: Scan dest 1 must be *float32 or *float64: %w", ErrSchemaViolation)
			}
		default:
			return fmt.Errorf("vec: Scan takes at most 2 destinations: %w", ErrSchemaViolation)
		}
	}
	return nil
}

// buildWhere substitutes positional ? arguments into a WHERE expression as SQL
// literals so the VectorSQL binder can lower it to a predicate.
func buildWhere(expr string, args []any) (string, error) {
	var sb strings.Builder
	ai := 0
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		if ch != '?' {
			sb.WriteByte(ch)
			continue
		}
		if ai >= len(args) {
			return "", fmt.Errorf("vec: not enough filter arguments: %w", ErrSchemaViolation)
		}
		lit, err := sqlLiteral(args[ai])
		if err != nil {
			return "", err
		}
		sb.WriteString(lit)
		ai++
	}
	if ai != len(args) {
		return "", fmt.Errorf("vec: too many filter arguments: %w", ErrSchemaViolation)
	}
	return sb.String(), nil
}

// sqlLiteral renders a Go value as a VectorSQL literal.
func sqlLiteral(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'", nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case time.Time:
		return strconv.FormatInt(x.UnixNano(), 10), nil
	default:
		return "", fmt.Errorf("vec: unsupported filter argument %T: %w", v, ErrSchemaViolation)
	}
}

// zeroLiteral renders a zero vector literal of the given dimension.
func zeroLiteral(dims int) string {
	if dims <= 0 {
		return "0"
	}
	parts := make([]string, dims)
	for i := range parts {
		parts[i] = "0"
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// mapSQLErr maps a VectorSQL binder error to the library vocabulary.
func mapSQLErr(err error) error {
	var ve *vectorsql.VecError
	if errors.As(err, &ve) {
		return fmt.Errorf("vec: %s: %w", ve.Message, ErrSchemaViolation)
	}
	return err
}
