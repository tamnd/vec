package cli

import (
	"strings"
	"testing"
)

// runScript drives the CLI in batch mode over a database path and returns stdout,
// stderr, and the exit code.
func runScript(t *testing.T, path, script string, flags ...string) (string, string, int) {
	t.Helper()
	var out, errOut strings.Builder
	args := append([]string{"-batch"}, flags...)
	args = append(args, path)
	code := run(args, strings.NewReader(script), &out, &errOut)
	return out.String(), errOut.String(), code
}

const setup = `CREATE TABLE docs (title TEXT, emb VECTOR(4));
INSERT INTO docs (id, title, emb) VALUES (1, 'alpha', '[1,0,0,0]');
INSERT INTO docs (id, title, emb) VALUES (2, 'beta', '[0,1,0,0]');
`

func TestCreateInsertQuery(t *testing.T) {
	script := setup + "SELECT title FROM docs ORDER BY emb <-> '[1,0,0,0]' LIMIT 2;\n"
	out, errOut, code := runScript(t, ":memory:", script, "-mode", "csv")
	if code != ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	// alpha is nearest to [1,0,0,0], so it is the first data row.
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[1], "1,alpha,") {
		t.Errorf("first result row = %q, want alpha first", lines[1])
	}
}

func TestHNSWIndexThroughShell(t *testing.T) {
	script := setup +
		"CREATE INDEX docs_emb_idx ON docs USING hnsw (emb) WITH (m=8, ef_construction=64);\n" +
		"SELECT title FROM docs ORDER BY emb <-> '[0,1,0,0]' LIMIT 1;\n"
	out, errOut, code := runScript(t, ":memory:", script, "-mode", "list", "-noheader")
	if code != ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if got := strings.TrimSpace(out); !strings.Contains(got, "beta") {
		t.Errorf("nearest to [0,1,0,0] = %q, want beta", got)
	}
}

func TestDotTablesAndSchema(t *testing.T) {
	out, _, code := runScript(t, ":memory:", setup+".tables\n.schema docs\n")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "docs") {
		t.Errorf(".tables did not list docs: %q", out)
	}
	if !strings.Contains(out, "VECTOR(4)") {
		t.Errorf(".schema did not show the vector column: %q", out)
	}
}

func TestDelete(t *testing.T) {
	script := setup + "DELETE FROM docs WHERE id = 1;\n.stats docs\n"
	out, errOut, code := runScript(t, ":memory:", script)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "docs: 1 points") {
		t.Errorf("want 1 point after delete, got %q", out)
	}
}

func TestMissingCollectionExitCode(t *testing.T) {
	_, _, code := runScript(t, ":memory:", "SELECT * FROM nope ORDER BY v <-> '[1]' LIMIT 1;\n")
	if code != ExitNotFound {
		t.Errorf("exit %d, want ExitNotFound %d", code, ExitNotFound)
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errOut strings.Builder
	code := run([]string{"-version"}, strings.NewReader(""), &out, &errOut)
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "vec 0.1.0") {
		t.Errorf("version output = %q", out.String())
	}
}

func TestUnknownFlagExitCode(t *testing.T) {
	var out, errOut strings.Builder
	code := run([]string{"-bogus", ":memory:"}, strings.NewReader(""), &out, &errOut)
	if code != ExitUsage {
		t.Errorf("exit %d, want ExitUsage %d", code, ExitUsage)
	}
}

func TestPragmaRead(t *testing.T) {
	out, errOut, code := runScript(t, ":memory:", "PRAGMA page_size;\n", "-mode", "list", "-noheader")
	if code != ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if strings.TrimSpace(out) != "4096" {
		t.Errorf("page_size = %q, want 4096", strings.TrimSpace(out))
	}
}

func TestRenderModes(t *testing.T) {
	tbl := table{cols: []string{"id", "title"}, rows: [][]string{{"1", "alpha"}, {"2", "beta"}}}
	for _, mode := range []string{"table", "box", "list", "csv", "tsv", "json", "jsonl", "line", "column", "markdown", "quote"} {
		var b strings.Builder
		if err := render(&b, mode, true, tbl); err != nil {
			t.Errorf("mode %q: %v", mode, err)
			continue
		}
		if b.Len() == 0 {
			t.Errorf("mode %q produced no output", mode)
		}
	}
	var b strings.Builder
	if err := render(&b, "nope", true, tbl); err == nil {
		t.Errorf("unknown mode should error")
	}
}

// nonEmptyLines splits s into its non-empty lines.
func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}
