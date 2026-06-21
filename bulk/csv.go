package bulk

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// CSVOptions tunes the CSV reader (spec 17 §2.2).
type CSVOptions struct {
	// Delimiter is the field separator (default ',').
	Delimiter rune
	// VectorColumn names a single column holding a bracketed float list. When
	// empty, the reader looks for a numbered run named by NumberedPrefix.
	VectorColumn string
	// NumberedPrefix is the prefix of a numbered vector run (e.g. "v" for
	// v0,v1,...). Used only when VectorColumn is empty.
	NumberedPrefix string
}

// csvSource streams rows from a CSV file with a header row (spec 17 §2.2).
type csvSource struct {
	r          *csv.Reader
	header     []string
	vecCol   int   // index of the single bracketed vector column, or -1
	numbered []int // indices of a numbered vector run, in order
	rowIdx   uint64
}

// NewCSVSource builds a CSV RowSource. It reads the header row immediately so a
// mapping error surfaces before any data is streamed.
func NewCSVSource(r io.Reader, opts CSVOptions) (RowSource, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	if opts.Delimiter != 0 {
		cr.Comma = opts.Delimiter
	}
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("bulk: read CSV header: %w", err)
	}
	head := append([]string(nil), header...)
	idx := make(map[string]int, len(head))
	for i, name := range head {
		idx[name] = i
	}
	s := &csvSource{r: cr, header: head, vecCol: -1}

	vecName := opts.VectorColumn
	switch {
	case vecName != "":
		col, ok := idx[vecName]
		if !ok {
			return nil, fmt.Errorf("bulk: CSV vector column %q not in header", vecName)
		}
		s.vecCol = col
	case opts.NumberedPrefix != "":
		s.numbered = numberedRun(head, opts.NumberedPrefix)
		if len(s.numbered) == 0 {
			return nil, fmt.Errorf("bulk: no CSV columns with prefix %q", opts.NumberedPrefix)
		}
	default:
		// Fall back to a column literally named "embedding" or "vector".
		for _, name := range []string{"embedding", "vector"} {
			if col, ok := idx[name]; ok {
				s.vecCol = col
				break
			}
		}
		if s.vecCol < 0 {
			return nil, fmt.Errorf("bulk: CSV vector column not found; set CSVOptions.VectorColumn")
		}
	}
	return s, nil
}

// numberedRun returns the column indices of a prefix+integer run sorted by the
// integer, so v0,v10,v2 orders as v0,v2,v10.
func numberedRun(header []string, prefix string) []int {
	type col struct {
		idx int
		n   int
	}
	var cols []col
	for i, name := range header {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		n, err := strconv.Atoi(name[len(prefix):])
		if err != nil {
			continue
		}
		cols = append(cols, col{idx: i, n: n})
	}
	sort.Slice(cols, func(a, b int) bool { return cols[a].n < cols[b].n })
	out := make([]int, len(cols))
	for i, c := range cols {
		out[i] = c.idx
	}
	return out
}

func (s *csvSource) Dim() int {
	if len(s.numbered) > 0 {
		return len(s.numbered)
	}
	return 0
}

func (s *csvSource) Next() (Row, error) {
	rec, err := s.r.Read()
	if err == io.EOF {
		return Row{}, errStop
	}
	if err != nil {
		return Row{}, err
	}
	row := Row{AutoIndex: s.rowIdx, Fields: make(map[string]any, len(s.header))}
	s.rowIdx++

	// Vector.
	if len(s.numbered) > 0 {
		vecf := make([]float32, len(s.numbered))
		for i, col := range s.numbered {
			if col >= len(rec) {
				return Row{}, fmt.Errorf("row %d: missing column index %d", row.AutoIndex, col)
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(rec[col]), 32)
			if err != nil {
				return Row{}, fmt.Errorf("row %d vector element %d: %w", row.AutoIndex, i, err)
			}
			vecf[i] = float32(f)
		}
		row.Vector = vecf
	} else {
		if s.vecCol >= len(rec) {
			return Row{}, fmt.Errorf("row %d: missing vector column", row.AutoIndex)
		}
		vecf, err := parseVectorString(rec[s.vecCol])
		if err != nil {
			return Row{}, fmt.Errorf("row %d vector: %w", row.AutoIndex, err)
		}
		row.Vector = vecf
	}

	// Fields (every named column, including the vector column as its raw string).
	for i, name := range s.header {
		if i < len(rec) {
			row.Fields[name] = rec[i]
		}
	}
	return row, nil
}

func (s *csvSource) Close() error { return nil }
