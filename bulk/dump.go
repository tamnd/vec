package bulk

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	vec "github.com/tamnd/vec"
)

// dumpFormatVersion is the logical dump format version (spec 17 §3.5). It is
// bumped only on a breaking change to the dump syntax, never on a vec version bump.
const dumpFormatVersion = 1

// DumpOptions controls a logical dump (spec 17 §3.3).
type DumpOptions struct {
	// Collections limits the dump to these collections; empty dumps all.
	Collections []string
	// NoIndexes omits CREATE INDEX statements (data and schema only).
	NoIndexes bool
	// NoData omits INSERT statements (schema only).
	NoData bool
	// BatchSize is the number of rows per INSERT statement (default 1000).
	BatchSize int
	// Compress wraps the output in gzip when set to "gzip". The empty string
	// writes plain text. zstd is not available in a stdlib-only build.
	Compress string
	// Header writes the leading comment block when true (default true via Dump).
	header bool
	// Now is the timestamp stamped into the header; the zero value omits it so a
	// dump is reproducible in tests.
	Now time.Time
}

func (o DumpOptions) batchSize() int {
	if o.BatchSize > 0 {
		return o.BatchSize
	}
	return 1000
}

// errWriter is a bufio.Writer wrapper that remembers the first write error so the
// dump code can format without checking every call; the error is read once at the
// end. bufio.Writer is already sticky, but the wrapper keeps the dump functions
// free of the unchecked-return lint without scattering blank assignments.
type errWriter struct {
	w   *bufio.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, a...)
	}
}

func (e *errWriter) writeString(s string) {
	if e.err == nil {
		_, e.err = e.w.WriteString(s)
	}
}

// line writes s followed by a newline; an empty s writes a blank line.
func (e *errWriter) line(s string) { e.printf("%s\n", s) }

// Dump writes a logical VectorSQL dump of db to w (spec 17 §3.2). The dump is a
// consistent point-in-time image: each collection is scanned at a snapshot taken
// when its scan begins. The output re-parses with the repository's own
// vectorsql.Parse and reloads with Load.
func Dump(ctx context.Context, db *vec.DB, w io.Writer, opts DumpOptions) error {
	opts.header = true
	out := w
	var gz *gzip.Writer
	if strings.EqualFold(opts.Compress, "gzip") {
		gz = gzip.NewWriter(w)
		out = gz
	} else if opts.Compress != "" {
		return fmt.Errorf("bulk: dump compression %q is not supported (use \"gzip\" or \"\")", opts.Compress)
	}
	ew := &errWriter{w: bufio.NewWriter(out)}

	if err := dumpTo(ctx, db, ew, opts); err != nil {
		return err
	}
	if ew.err != nil {
		return ew.err
	}
	if err := ew.w.Flush(); err != nil {
		return err
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}

func dumpTo(ctx context.Context, db *vec.DB, w *errWriter, opts DumpOptions) error {
	names, err := selectedCollections(ctx, db, opts.Collections)
	if err != nil {
		return err
	}
	if opts.header {
		writeDumpHeader(w, opts.Now)
	}
	for _, name := range names {
		if err := dumpCollection(ctx, db, w, name, opts); err != nil {
			return err
		}
	}
	w.line("COMMIT;")
	return nil
}

// selectedCollections resolves the dump's collection set, preserving the catalog
// order and validating any explicit names.
func selectedCollections(ctx context.Context, db *vec.DB, want []string) ([]string, error) {
	infos, err := db.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	all := make([]string, len(infos))
	have := make(map[string]bool, len(infos))
	for i, info := range infos {
		all[i] = info.Name
		have[info.Name] = true
	}
	if len(want) == 0 {
		return all, nil
	}
	for _, n := range want {
		if !have[n] {
			return nil, fmt.Errorf("bulk: collection %q not found", n)
		}
	}
	return want, nil
}

func writeDumpHeader(w *errWriter, now time.Time) {
	w.printf("-- vec dump format %d\n", dumpFormatVersion)
	w.printf("-- vec version: %s\n", vec.Version())
	if !now.IsZero() {
		w.printf("-- dumped at: %s\n", now.UTC().Format(time.RFC3339))
	}
	w.line("")
}

func dumpCollection(ctx context.Context, db *vec.DB, w *errWriter, name string, opts DumpOptions) error {
	info, err := db.GetCollection(ctx, name)
	if err != nil {
		return err
	}
	writeCreateTable(w, info)
	w.line("")

	if !opts.NoIndexes {
		idxs, err := db.ListIndexes(ctx, name)
		if err != nil {
			return err
		}
		metric := vectorMetric(info)
		vecCol := vectorColumnName(info)
		coversVec := false
		for _, idx := range idxs {
			writeCreateIndex(w, name, idx, metric)
			if idx.Column == vecCol {
				coversVec = true
			}
		}
		emitted := len(idxs) > 0
		// The metric rides on the index opclass. When the collection has no index on
		// its vector column and the metric is not the load default, emit a flat
		// carrier so the metric survives the round trip. Flat is the brute-force
		// access path every collection already supports, so this adds no real cost.
		if !coversVec && vecCol != "" && metric != vec.MetricL2 {
			writeFlatMetricCarrier(w, name, vecCol, metric)
			emitted = true
		}
		if emitted {
			w.line("")
		}
	}

	if opts.NoData {
		return nil
	}
	return dumpData(ctx, db, w, name, info, opts)
}

// writeCreateTable renders a CREATE TABLE that re-parses with vectorsql.Parse. The
// metric is not written on the column (the parser is pgvector-style and carries no
// metric on the type); it is recovered on load from the CREATE INDEX opclass.
func writeCreateTable(w *errWriter, info vec.CollectionInfo) {
	w.printf("CREATE TABLE %s (\n", quoteIdent(info.Name))
	for i, c := range info.Columns {
		comma := ","
		if i == len(info.Columns)-1 {
			comma = ""
		}
		w.printf("  %s %s%s%s\n", quoteIdent(c.Name), columnTypeSQL(c), columnConstraintsSQL(c), comma)
	}
	w.line(");")
}

func columnTypeSQL(c vec.ColumnDef) string {
	switch c.Type {
	case vec.TypeVector:
		return fmt.Sprintf("VECTOR(%d)", c.Dim)
	case vec.TypeInt64:
		return "BIGINT"
	case vec.TypeFloat64:
		return "DOUBLE PRECISION"
	case vec.TypeBool:
		return "BOOLEAN"
	case vec.TypeText:
		return "TEXT"
	case vec.TypeBytes:
		return "BYTEA"
	case vec.TypeJSON:
		return "JSON"
	case vec.TypeTimestamp:
		return "TIMESTAMP"
	default:
		return "TEXT"
	}
}

func columnConstraintsSQL(c vec.ColumnDef) string {
	var b strings.Builder
	// The integer id column is the primary key in every facade collection.
	if c.Type == vec.TypeInt64 && (c.Name == "id" || c.Name == "point_id") {
		b.WriteString(" PRIMARY KEY")
	}
	if c.NotNull {
		b.WriteString(" NOT NULL")
	}
	return b.String()
}

// writeCreateIndex renders a CREATE INDEX with the opclass that carries the metric.
func writeCreateIndex(w *errWriter, table string, idx vec.IndexInfo, metric vec.Metric) {
	opclass := opclassForMetric(metric)
	w.printf("CREATE INDEX ON %s USING %s (%s %s)",
		quoteIdent(table), idx.Type.String(), quoteIdent(idx.Column), opclass)
	if with := indexWithClause(idx.Params); with != "" {
		w.printf(" WITH (%s)", with)
	}
	w.line(";")
}

// writeFlatMetricCarrier emits a flat index whose only purpose is to carry the
// vector column metric through the dump's opclass.
func writeFlatMetricCarrier(w *errWriter, table, column string, metric vec.Metric) {
	w.printf("CREATE INDEX ON %s USING flat (%s %s);\n",
		quoteIdent(table), quoteIdent(column), opclassForMetric(metric))
}

// vectorColumnName returns the name of the collection's vector column, or "".
func vectorColumnName(info vec.CollectionInfo) string {
	for _, c := range info.Columns {
		if c.Type == vec.TypeVector {
			return c.Name
		}
	}
	return ""
}

// indexWithClause renders the WITH options in a stable key order.
func indexWithClause(p vec.IndexParams) string {
	if len(p) == 0 {
		return ""
	}
	order := []string{"m", "ef_construction", "nlist", "nprobe", "pq_m"}
	var parts []string
	seen := map[string]bool{}
	for _, k := range order {
		if v, ok := p[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%s", k, paramLiteral(v)))
			seen[k] = true
		}
	}
	for k, v := range p {
		if seen[k] {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, paramLiteral(v)))
	}
	return strings.Join(parts, ", ")
}

func paramLiteral(v any) string {
	switch x := v.(type) {
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	default:
		return fmt.Sprintf("%v", x)
	}
}

func dumpData(ctx context.Context, db *vec.DB, w *errWriter, name string, info vec.CollectionInfo, opts DumpOptions) error {
	coll, err := db.Collection(name)
	if err != nil {
		return err
	}
	cols := info.Columns
	colList := make([]string, len(cols))
	for i, c := range cols {
		colList[i] = quoteIdent(c.Name)
	}
	insertPrefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES\n", quoteIdent(name), strings.Join(colList, ", "))

	batch := opts.batchSize()
	count := 0
	var rowBuf strings.Builder

	flush := func() error {
		if count == 0 {
			return nil
		}
		w.writeString(insertPrefix)
		w.writeString(rowBuf.String())
		w.writeString(";\n")
		rowBuf.Reset()
		count = 0
		return w.err
	}

	scanErr := coll.Scan(ctx, func(p vec.Point) error {
		if count > 0 {
			rowBuf.WriteString(",\n")
		}
		rowBuf.WriteString("  (")
		rowBuf.WriteString(renderRow(p, cols))
		rowBuf.WriteString(")")
		count++
		if count >= batch {
			return flush()
		}
		return nil
	})
	if scanErr != nil {
		return scanErr
	}
	if err := flush(); err != nil {
		return err
	}
	w.line("")
	return nil
}

// renderRow renders one VALUES tuple in column order.
func renderRow(p vec.Point, cols []vec.ColumnDef) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		switch c.Type {
		case vec.TypeVector:
			parts[i] = renderVectorLiteral(p, c.Name)
		case vec.TypeInt64:
			if c.Name == "id" || c.Name == "point_id" {
				parts[i] = strconv.FormatUint(p.ID.N, 10)
				continue
			}
			parts[i] = renderValue(p.Meta[c.Name])
		default:
			parts[i] = renderValue(p.Meta[c.Name])
		}
	}
	return strings.Join(parts, ", ")
}

func renderVectorLiteral(p vec.Point, col string) string {
	av, ok := p.Vectors[col]
	if !ok {
		for _, v := range p.Vectors {
			av = v
			ok = true
			break
		}
	}
	if !ok || av.Dense == nil {
		return "NULL"
	}
	var b strings.Builder
	b.WriteByte('\'')
	b.WriteByte('[')
	for i, x := range av.Dense {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	b.WriteByte('\'')
	return b.String()
}

// renderValue renders a metadata value as a VectorSQL literal.
func renderValue(v vec.Value) string {
	if v.IsNull() {
		return "NULL"
	}
	switch v.Type() {
	case vec.TypeInt64:
		return strconv.FormatInt(v.Int(), 10)
	case vec.TypeFloat64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case vec.TypeBool:
		if v.Bool() {
			return "TRUE"
		}
		return "FALSE"
	case vec.TypeText, vec.TypeJSON:
		return "'" + strings.ReplaceAll(v.Text(), "'", "''") + "'"
	case vec.TypeBytes:
		return "'\\x" + hex.EncodeToString(v.Bytes()) + "'"
	case vec.TypeTimestamp:
		return "'" + v.Time().UTC().Format(time.RFC3339Nano) + "'"
	default:
		return "NULL"
	}
}

// quoteIdent wraps an identifier in double quotes only when it needs them.
func quoteIdent(s string) string {
	if isBareIdent(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func isBareIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
