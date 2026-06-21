package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// table is a rendered result set: an ordered list of column names and string cells.
// The SQL runner fills it from a query cursor, the renderer turns it into text.
type table struct {
	cols []string
	rows [][]string
}

// resolveMode picks the output mode. An explicit mode wins; otherwise a TTY gets the
// boxed table and a pipe gets jsonl so piped output stays machine readable.
func resolveMode(mode string, tty bool) string {
	if mode != "" {
		return mode
	}
	if tty {
		return "table"
	}
	return "jsonl"
}

// render writes t to w in the named mode.
func render(w io.Writer, mode string, headers bool, t table) error {
	switch mode {
	case "table", "box":
		return renderBox(w, headers, t)
	case "list":
		return renderList(w, headers, t, "|")
	case "tabs", "tsv":
		return renderList(w, headers, t, "\t")
	case "csv":
		return renderCSV(w, headers, t)
	case "json":
		return renderJSON(w, t, false)
	case "jsonl", "ndjson":
		return renderJSON(w, t, true)
	case "line", "lines":
		return renderLine(w, t)
	case "column":
		return renderColumn(w, headers, t)
	case "markdown", "md":
		return renderMarkdown(w, t)
	case "quote":
		return renderQuote(w, headers, t)
	default:
		return fmt.Errorf("unknown output mode %q", mode)
	}
}

// colWidths returns the display width of each column across the header and rows.
func colWidths(headers bool, t table) []int {
	w := make([]int, len(t.cols))
	if headers {
		for i, c := range t.cols {
			w[i] = len(c)
		}
	}
	for _, r := range t.rows {
		for i, cell := range r {
			if i < len(w) && len(cell) > w[i] {
				w[i] = len(cell)
			}
		}
	}
	return w
}

// renderBox draws the SQLite-style boxed grid.
func renderBox(w io.Writer, headers bool, t table) error {
	if len(t.cols) == 0 {
		return nil
	}
	width := colWidths(headers, t)
	bar := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(left)
		for i, n := range width {
			b.WriteString(strings.Repeat("─", n+2))
			if i < len(width)-1 {
				b.WriteString(mid)
			}
		}
		b.WriteString(right)
		b.WriteByte('\n')
		return b.String()
	}
	line := func(cells []string) string {
		var b strings.Builder
		b.WriteString("│")
		for i, n := range width {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			b.WriteString(" " + cell + strings.Repeat(" ", n-len(cell)) + " │")
		}
		b.WriteByte('\n')
		return b.String()
	}
	if _, err := io.WriteString(w, bar("┌", "┬", "┐")); err != nil {
		return err
	}
	if headers {
		if _, err := io.WriteString(w, line(t.cols)); err != nil {
			return err
		}
		if _, err := io.WriteString(w, bar("├", "┼", "┤")); err != nil {
			return err
		}
	}
	for _, r := range t.rows {
		if _, err := io.WriteString(w, line(r)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, bar("└", "┴", "┘"))
	return err
}

// renderColumn prints left-aligned padded columns with a dashed underline, the
// classic sqlite3 .mode column look.
func renderColumn(w io.Writer, headers bool, t table) error {
	width := colWidths(headers, t)
	pad := func(cells []string) string {
		parts := make([]string, len(width))
		for i, n := range width {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			parts[i] = cell + strings.Repeat(" ", n-len(cell))
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	if headers {
		if _, err := fmt.Fprintln(w, pad(t.cols)); err != nil {
			return err
		}
		dash := make([]string, len(width))
		for i, n := range width {
			dash[i] = strings.Repeat("-", n)
		}
		if _, err := fmt.Fprintln(w, pad(dash)); err != nil {
			return err
		}
	}
	for _, r := range t.rows {
		if _, err := fmt.Fprintln(w, pad(r)); err != nil {
			return err
		}
	}
	return nil
}

// renderList prints rows joined by sep, the sqlite3 .mode list and .mode tabs forms.
func renderList(w io.Writer, headers bool, t table, sep string) error {
	if headers {
		if _, err := fmt.Fprintln(w, strings.Join(t.cols, sep)); err != nil {
			return err
		}
	}
	for _, r := range t.rows {
		if _, err := fmt.Fprintln(w, strings.Join(r, sep)); err != nil {
			return err
		}
	}
	return nil
}

// renderCSV writes RFC 4180 CSV.
func renderCSV(w io.Writer, headers bool, t table) error {
	cw := csv.NewWriter(w)
	if headers {
		if err := cw.Write(t.cols); err != nil {
			return err
		}
	}
	for _, r := range t.rows {
		if err := cw.Write(r); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// renderJSON writes one object per row, either as a JSON array or as JSON lines.
func renderJSON(w io.Writer, t table, lines bool) error {
	enc := json.NewEncoder(w)
	if lines {
		for _, r := range t.rows {
			if err := enc.Encode(rowObject(t.cols, r)); err != nil {
				return err
			}
		}
		return nil
	}
	objs := make([]map[string]string, 0, len(t.rows))
	for _, r := range t.rows {
		objs = append(objs, rowObject(t.cols, r))
	}
	return enc.Encode(objs)
}

// rowObject pairs column names with cell values for JSON output.
func rowObject(cols, row []string) map[string]string {
	obj := make(map[string]string, len(cols))
	for i, c := range cols {
		if i < len(row) {
			obj[c] = row[i]
		}
	}
	return obj
}

// renderLine prints each row as a block of name = value lines, blank line between
// rows, the sqlite3 .mode line form.
func renderLine(w io.Writer, t table) error {
	width := 0
	for _, c := range t.cols {
		if len(c) > width {
			width = len(c)
		}
	}
	for ri, r := range t.rows {
		if ri > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		for i, c := range t.cols {
			cell := ""
			if i < len(r) {
				cell = r[i]
			}
			if _, err := fmt.Fprintf(w, "%*s = %s\n", width, c, cell); err != nil {
				return err
			}
		}
	}
	return nil
}

// renderMarkdown writes a GitHub-flavored markdown table.
func renderMarkdown(w io.Writer, t table) error {
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(t.cols, " | ")); err != nil {
		return err
	}
	sep := make([]string, len(t.cols))
	for i := range sep {
		sep[i] = "---"
	}
	if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(sep, " | ")); err != nil {
		return err
	}
	for _, r := range t.rows {
		if _, err := fmt.Fprintf(w, "| %s |\n", strings.Join(r, " | ")); err != nil {
			return err
		}
	}
	return nil
}

// renderQuote writes each cell as a single-quoted SQL literal, comma separated.
func renderQuote(w io.Writer, headers bool, t table) error {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	if headers {
		qc := make([]string, len(t.cols))
		for i, c := range t.cols {
			qc[i] = q(c)
		}
		if _, err := fmt.Fprintln(w, strings.Join(qc, ",")); err != nil {
			return err
		}
	}
	for _, r := range t.rows {
		qr := make([]string, len(r))
		for i, c := range r {
			qr[i] = q(c)
		}
		if _, err := fmt.Fprintln(w, strings.Join(qr, ",")); err != nil {
			return err
		}
	}
	return nil
}

// formatFloat renders a distance or score without trailing zero noise.
func formatFloat(f float32) string {
	return strconv.FormatFloat(float64(f), 'g', 6, 32)
}
