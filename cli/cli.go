package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tamnd/vec"
)

// Main is the entry point cmd/vec calls. It parses the global flags, dispatches to a
// subcommand or the shell, and returns the process exit code.
func Main(args []string) int {
	return run(args, os.Stdin, stdout, stderr)
}

// run is Main with injectable streams so tests can drive the shell.
func run(args []string, in io.Reader, out, errOut io.Writer) int {
	cfg, rest, err := parseGlobals(args)
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(errOut, "vec:", ue.msg)
			return ExitUsage
		}
		fmt.Fprintln(errOut, "vec:", err)
		return ExitUsage
	}
	if cfg.showVersion {
		ver, commit, date := vec.BuildInfo()
		line := "vec " + ver
		if commit != "" {
			line += " (" + commit + ")"
		}
		if date != "" {
			line += " " + date
		}
		fmt.Fprintln(out, line)
		return ExitOK
	}
	if cfg.showHelp {
		printUsage(out)
		return ExitOK
	}

	if len(rest) > 0 && isSubcommand(rest[0]) {
		return runSubcommand(cfg, rest[0], rest[1:], out, errOut)
	}

	// Positional form: the first remaining argument is the database, anything after
	// it is a single SQL string to run in batch.
	var sqlArgs []string
	if len(rest) > 0 {
		cfg.path = rest[0]
		sqlArgs = rest[1:]
	}

	db, err := openDatabase(cfg)
	if err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	interactive := !cfg.batch && len(sqlArgs) == 0 && isTerminal(in)

	if cfg.initFile != "" {
		if code := runInitFile(ctx, db, &cfg, out, errOut); code != ExitOK {
			return code
		}
	}

	if len(sqlArgs) > 0 {
		sh := newShell(ctx, db, &cfg, out, errOut, false)
		return sh.run(strings.NewReader(strings.Join(sqlArgs, " ")))
	}

	sh := newShell(ctx, db, &cfg, out, errOut, interactive)
	if interactive {
		fmt.Fprintln(out, "vec "+vec.Version()+"  .help for commands, .quit to exit")
	}
	return sh.run(in)
}

// runInitFile runs the statements in cfg.initFile as a batch before the shell starts.
func runInitFile(ctx context.Context, db *vec.DB, cfg *config, out, errOut io.Writer) int {
	f, err := os.Open(cfg.initFile)
	if err != nil {
		fmt.Fprintln(errOut, "vec:", err)
		return exitForError(err)
	}
	defer func() { _ = f.Close() }()
	sh := newShell(ctx, db, cfg, out, errOut, false)
	return sh.run(f)
}

// isTerminal reports whether r is an interactive character device.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
