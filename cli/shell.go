package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tamnd/vec"
)

// shell is one interactive or batch session over an open database. It owns the
// output settings the dot-commands mutate and the statement buffer the reader fills.
type shell struct {
	ctx         context.Context
	db          *vec.DB
	cfg         *config
	out         io.Writer
	errOut      io.Writer
	mode        string
	headers     bool
	nullval     string
	echo        bool
	interactive bool
	quit        bool
}

// newShell builds a session from the resolved config and an open database.
func newShell(ctx context.Context, db *vec.DB, cfg *config, out, errOut io.Writer, interactive bool) *shell {
	return &shell{
		ctx:         ctx,
		db:          db,
		cfg:         cfg,
		out:         out,
		errOut:      errOut,
		mode:        resolveMode(cfg.mode, interactive),
		headers:     cfg.headers,
		nullval:     cfg.nullText,
		interactive: interactive,
	}
}

// run reads statements from in until EOF or .quit and returns the exit code. A
// statement error in an interactive session is printed and the loop continues; in a
// batch session it stops the run and sets the exit code.
func (sh *shell) run(in io.Reader) int {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	code := ExitOK
	var buf strings.Builder

	for {
		empty := strings.TrimSpace(buf.String()) == ""
		sh.prompt(empty)
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		if empty {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				buf.Reset()
				continue
			}
			if strings.HasPrefix(trimmed, ".") {
				if err := sh.dispatchDot(trimmed); err != nil {
					code = sh.report(err)
					if !sh.interactive {
						return code
					}
				}
				if sh.quit {
					return code
				}
				continue
			}
		}
		if sh.echo {
			fmt.Fprintln(sh.errOut, line)
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		// Drain every complete statement currently in the buffer.
		rest, c, stop := sh.drain(buf.String())
		buf.Reset()
		if strings.TrimSpace(rest) != "" {
			buf.WriteString(rest)
		}
		if c != ExitOK {
			code = c
		}
		if stop {
			return code
		}
	}
	if rest := strings.TrimSpace(buf.String()); rest != "" {
		if _, _, stop := sh.drain(rest + ";"); stop {
			return code
		}
	}
	return code
}

// drain runs each ';'-terminated statement in s, returning the unfinished remainder,
// the last non-zero exit code, and whether a batch run should stop.
func (sh *shell) drain(s string) (rest string, code int, stop bool) {
	code = ExitOK
	for {
		i := strings.IndexByte(s, ';')
		if i < 0 {
			return s, code, false
		}
		stmt := strings.TrimSpace(s[:i])
		s = s[i+1:]
		if stmt == "" {
			continue
		}
		if err := sh.exec(stmt); err != nil {
			code = sh.report(err)
			if !sh.interactive {
				return "", code, true
			}
		}
		if sh.quit {
			return "", code, true
		}
	}
}

// exec runs one statement and renders its result.
func (sh *shell) exec(stmt string) error {
	res, err := runStatement(sh.ctx, sh.db, stmt)
	if err != nil {
		return err
	}
	if res.tbl != nil {
		return render(sh.out, sh.mode, sh.headers, *res.tbl)
	}
	if res.status != "" && sh.interactive {
		fmt.Fprintln(sh.out, res.status)
	}
	return nil
}

// prompt writes the primary or continuation prompt in an interactive session.
func (sh *shell) prompt(primary bool) {
	if !sh.interactive {
		return
	}
	if primary {
		fmt.Fprint(sh.out, "vec> ")
	} else {
		fmt.Fprint(sh.out, "...> ")
	}
}

// report prints an error and returns its exit code.
func (sh *shell) report(err error) int {
	fmt.Fprintln(sh.errOut, "Error:", err)
	return exitForError(err)
}
