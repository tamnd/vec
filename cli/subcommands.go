package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tamnd/vec"
)

// isSubcommand reports whether name is a reserved subcommand, which decides the
// dispatch between "vec <subcommand>" and "vec <database>".
func isSubcommand(name string) bool {
	switch name {
	case "create", "stats", "indexes", "tables", "import", "export",
		"build", "reindex", "check", "vacuum", "backup", "restore", "bench", "serve", "version", "help":
		return true
	default:
		return false
	}
}

// runSubcommand runs a reserved subcommand and returns its exit code.
func runSubcommand(cfg config, name string, args []string, out, errOut io.Writer) int {
	switch name {
	case "version":
		fmt.Fprintln(out, "vec", vec.Version())
		return ExitOK
	case "help":
		printUsage(out)
		return ExitOK
	case "serve":
		fmt.Fprintln(errOut, "vec: serve is delivered by the server subsystem (spec 16)")
		return ExitUsage
	case "import", "export", "backup", "restore", "vacuum", "bench":
		fmt.Fprintf(errOut, "vec: %s is delivered by a later subsystem (specs 17, 18, 22)\n", name)
		return ExitUsage
	}

	// The remaining subcommands all open the database first.
	db, err := openDatabase(cfg)
	if err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	switch name {
	case "create":
		return subCreate(ctx, db, args, out, errOut)
	case "stats":
		return subStats(ctx, db, args, out, errOut)
	case "tables":
		return subTables(ctx, db, out, errOut)
	case "indexes":
		return subIndexes(ctx, db, args, out, errOut)
	case "build", "reindex":
		return subBuild(ctx, db, args, out, errOut)
	case "check":
		fmt.Fprintln(out, "ok")
		return ExitOK
	default:
		fmt.Fprintln(errOut, "vec: unknown subcommand", name)
		return ExitUsage
	}
}

// subCreate runs a CREATE TABLE statement given on the command line.
func subCreate(ctx context.Context, db *vec.DB, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: vec create <database> \"CREATE TABLE ...\"")
		return ExitUsage
	}
	stmt := strings.Join(args, " ")
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "CREATE") {
		stmt = "CREATE TABLE " + stmt
	}
	if _, err := runStatement(ctx, db, stmt); err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	fmt.Fprintln(out, "CREATE TABLE")
	return ExitOK
}

// subStats prints point and index stats for one or all collections.
func subStats(ctx context.Context, db *vec.DB, args []string, out, errOut io.Writer) int {
	sh := newShell(ctx, db, &config{}, out, errOut, false)
	if err := sh.dotStats(args); err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	return ExitOK
}

// subTables lists collections.
func subTables(ctx context.Context, db *vec.DB, out, errOut io.Writer) int {
	sh := newShell(ctx, db, &config{}, out, errOut, false)
	if err := sh.dotTables(); err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	return ExitOK
}

// subIndexes lists indexes.
func subIndexes(ctx context.Context, db *vec.DB, args []string, out, errOut io.Writer) int {
	sh := newShell(ctx, db, &config{}, out, errOut, false)
	if err := sh.dotIndexes(args); err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	return ExitOK
}

// subBuild rebuilds the index on a collection.
func subBuild(ctx context.Context, db *vec.DB, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: vec build <database> <collection>")
		return ExitUsage
	}
	if err := db.BuildIndex(ctx, args[0]); err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	fmt.Fprintln(out, "built", args[0])
	return ExitOK
}

// openDatabase opens the database named by cfg with the resolved options.
func openDatabase(cfg config) (*vec.DB, error) {
	opts := []vec.Option{vec.WithBusyTimeout(cfg.busyTimeout)}
	if cfg.readOnly {
		return vec.OpenReadOnly(cfg.path, opts...)
	}
	return vec.Open(cfg.path, opts...)
}

// printUsage writes the top-level help.
func printUsage(w io.Writer) {
	const usage = `vec is a single-file vector database.

usage:
  vec [options] [database] [sql]      open a shell, or run sql and exit
  vec <subcommand> [args]

options:
  -mode MODE        output mode for query results
  -readonly         open the database read-only
  -init FILE        run FILE before the interactive shell
  -timeout DUR      busy timeout for writers (default 5s)
  -batch            force non-interactive mode
  -version          print the version
  -help             print this help

subcommands:
  create   create a collection from a CREATE TABLE statement
  tables   list collections
  indexes  list indexes
  stats    show collection and index stats
  build    rebuild the index on a collection
  check    verify the database opens
  version  print the version

The shell understands kNN SELECT, CREATE TABLE, CREATE INDEX, INSERT, DELETE,
DROP, and PRAGMA, plus dot-commands; type .help inside the shell.`
	fmt.Fprintln(w, usage)
}

// stdoutOrStderr is a tiny helper so callers can default writers in tests.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)
