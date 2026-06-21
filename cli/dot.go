package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tamnd/vec"
)

// dispatchDot runs one dot-command line. The leading dot is already present. An
// unknown command is an error the shell reports without stopping an interactive run.
func (sh *shell) dispatchDot(line string) error {
	fields := strings.Fields(line)
	cmd := fields[0]
	args := fields[1:]
	switch cmd {
	case ".help", ".h", ".?":
		return sh.dotHelp()
	case ".quit", ".exit", ".q":
		sh.quit = true
		return nil
	case ".mode":
		return sh.dotMode(args)
	case ".headers", ".header":
		return sh.dotToggle(args, &sh.headers)
	case ".echo":
		return sh.dotToggle(args, &sh.echo)
	case ".nullvalue", ".null":
		if len(args) > 0 {
			sh.nullval = args[0]
		}
		return nil
	case ".tables", ".collections":
		return sh.dotTables()
	case ".schema":
		return sh.dotSchema(args)
	case ".indexes", ".indices":
		return sh.dotIndexes(args)
	case ".stats":
		return sh.dotStats(args)
	case ".databases", ".database":
		fmt.Fprintln(sh.out, sh.db.Path())
		return nil
	case ".open":
		return sh.dotOpen(args)
	case ".read", ".source":
		return sh.dotRead(args)
	case ".output", ".out":
		return sh.dotOutput(args)
	case ".show":
		return sh.dotShow()
	case ".version":
		fmt.Fprintln(sh.out, "vec", vec.Version())
		return nil
	default:
		return fmt.Errorf("unknown command %q, try .help", cmd)
	}
}

// dotHelp prints the supported dot-commands.
func (sh *shell) dotHelp() error {
	const help = `.help              show this help
.tables            list collections
.schema [TABLE]    show a collection schema
.indexes [TABLE]   list indexes
.stats [TABLE]     show collection and index stats
.mode MODE         set output mode (table box list csv tsv json jsonl line column markdown quote)
.headers on|off    toggle column headers
.nullvalue TEXT    text printed for NULL
.echo on|off       echo input lines
.output FILE       send output to FILE, or stdout to reset
.open PATH         open a different database
.read FILE         run statements from FILE
.databases         print the open database path
.show              print current settings
.version           print the version
.quit              exit`
	fmt.Fprintln(sh.out, help)
	return nil
}

// dotMode sets the output mode after checking it renders.
func (sh *shell) dotMode(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(sh.out, "current mode:", sh.mode)
		return nil
	}
	m := strings.ToLower(args[0])
	if !validMode(m) {
		return fmt.Errorf("unknown output mode %q", m)
	}
	sh.mode = m
	return nil
}

// validMode reports whether name is a renderable output mode.
func validMode(name string) bool {
	switch name {
	case "table", "box", "list", "tabs", "tsv", "csv", "json", "jsonl", "ndjson",
		"line", "lines", "column", "markdown", "md", "quote":
		return true
	default:
		return false
	}
}

// dotToggle reads an on/off argument into a bool, flipping when no argument is given.
func (sh *shell) dotToggle(args []string, target *bool) error {
	if len(args) == 0 {
		*target = !*target
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "on", "yes", "true", "1":
		*target = true
	case "off", "no", "false", "0":
		*target = false
	default:
		return fmt.Errorf("expected on or off, got %q", args[0])
	}
	return nil
}

// dotTables lists collections in name order.
func (sh *shell) dotTables() error {
	infos, err := sh.db.ListCollections(sh.ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(infos))
	for _, in := range infos {
		names = append(names, in.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintln(sh.out, n)
	}
	return nil
}

// dotSchema prints the column definitions of one or all collections.
func (sh *shell) dotSchema(args []string) error {
	infos, err := sh.db.ListCollections(sh.ctx)
	if err != nil {
		return err
	}
	want := ""
	if len(args) > 0 {
		want = args[0]
	}
	for _, in := range infos {
		if want != "" && in.Name != want {
			continue
		}
		fmt.Fprintf(sh.out, "CREATE TABLE %s (\n", in.Name)
		fmt.Fprintln(sh.out, "  id BIGINT PRIMARY KEY,")
		for i, c := range in.Columns {
			tail := ","
			if i == len(in.Columns)-1 {
				tail = ""
			}
			fmt.Fprintf(sh.out, "  %s%s\n", columnSQL(c), tail)
		}
		fmt.Fprintln(sh.out, ");")
	}
	return nil
}

// columnSQL renders one column back to a CREATE TABLE fragment.
func columnSQL(c vec.ColumnDef) string {
	var b strings.Builder
	b.WriteString(c.Name)
	b.WriteByte(' ')
	if c.Type == vec.TypeVector {
		fmt.Fprintf(&b, "VECTOR(%d)", c.Dim)
	} else {
		b.WriteString(strings.ToUpper(c.Type.String()))
	}
	if c.NotNull {
		b.WriteString(" NOT NULL")
	}
	return b.String()
}

// dotIndexes lists the indexes of one or all collections.
func (sh *shell) dotIndexes(args []string) error {
	names, err := sh.collectionNames(args)
	if err != nil {
		return err
	}
	for _, name := range names {
		idxs, err := sh.db.ListIndexes(sh.ctx, name)
		if err != nil {
			return err
		}
		for _, ix := range idxs {
			fmt.Fprintf(sh.out, "%s on %s(%s) %s\n", ix.Name, name, ix.Column, ix.Type)
		}
	}
	return nil
}

// dotStats prints point counts and index stats.
func (sh *shell) dotStats(args []string) error {
	names, err := sh.collectionNames(args)
	if err != nil {
		return err
	}
	for _, name := range names {
		info, err := sh.db.GetCollection(sh.ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(sh.out, "%s: %d points\n", name, info.PointCount)
		if st, err := sh.db.IndexStats(sh.ctx, name); err == nil {
			fmt.Fprintf(sh.out, "  index %s (%s): %d nodes, %d tombstones, %d bytes\n",
				st.Name, st.Type, st.NodeCount, st.TombstoneCount, st.MemoryBytes)
		}
	}
	return nil
}

// collectionNames returns the requested collection name, or all of them sorted.
func (sh *shell) collectionNames(args []string) ([]string, error) {
	if len(args) > 0 {
		return []string{args[0]}, nil
	}
	infos, err := sh.db.ListCollections(sh.ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, in := range infos {
		names = append(names, in.Name)
	}
	sort.Strings(names)
	return names, nil
}

// dotOpen closes the current database and opens another.
func (sh *shell) dotOpen(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(".open needs a path")
	}
	cfg := *sh.cfg
	cfg.path = args[0]
	db, err := openDatabase(cfg)
	if err != nil {
		return err
	}
	_ = sh.db.Close()
	sh.db = db
	sh.cfg.path = args[0]
	return nil
}

// dotRead runs the statements in a file.
func (sh *shell) dotRead(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(".read needs a file")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sub := newShell(sh.ctx, sh.db, sh.cfg, sh.out, sh.errOut, false)
	sub.mode = sh.mode
	sub.headers = sh.headers
	sub.run(f)
	return nil
}

// dotOutput redirects rendered output to a file, or back to stdout.
func (sh *shell) dotOutput(args []string) error {
	if len(args) == 0 || args[0] == "stdout" || args[0] == "-" {
		sh.out = os.Stdout
		return nil
	}
	f, err := os.Create(args[0])
	if err != nil {
		return err
	}
	sh.out = f
	return nil
}

// dotShow prints the current session settings.
func (sh *shell) dotShow() error {
	fmt.Fprintln(sh.out, "database:", sh.db.Path())
	fmt.Fprintln(sh.out, "mode:    ", sh.mode)
	fmt.Fprintln(sh.out, "headers: ", onOff(sh.headers))
	fmt.Fprintln(sh.out, "nullvalue:", fmt.Sprintf("%q", sh.nullval))
	fmt.Fprintln(sh.out, "echo:    ", onOff(sh.echo))
	return nil
}

// onOff renders a bool as on or off.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
