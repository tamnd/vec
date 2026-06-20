package cli

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// config holds the settings the shell and subcommands run with. Flags override
// environment variables, which override the built-in defaults.
type config struct {
	path        string        // database file, or :memory:
	readOnly    bool          // open the file read-only
	mode        string        // output mode for SELECT results
	headers     bool          // print column headers in table/csv/tsv modes
	nullText    string        // text printed for a NULL cell
	busyTimeout time.Duration // how long a write waits for the lock
	initFile    string        // script run before an interactive session
	noColor     bool          // disable ANSI color even on a TTY
	batch       bool          // forced non-interactive mode

	showVersion bool
	showHelp    bool
}

// defaultConfig returns the settings before flags and environment are applied.
func defaultConfig() config {
	return config{
		path:        ":memory:",
		mode:        "", // resolved at render time: table on a TTY, jsonl on a pipe
		headers:     true,
		nullText:    "",
		busyTimeout: 5 * time.Second,
	}
}

// applyEnv folds the VEC_* environment variables into cfg. Flags are applied after
// this, so a flag always wins.
func (cfg *config) applyEnv() {
	if v := os.Getenv("VEC_MODE"); v != "" {
		cfg.mode = v
	}
	if v := os.Getenv("VEC_DATABASE"); v != "" {
		cfg.path = v
	}
	if v := os.Getenv("VEC_NULL"); v != "" {
		cfg.nullText = v
	}
	if v := os.Getenv("VEC_BUSY_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.busyTimeout = d
		}
	}
	if v := os.Getenv("NO_COLOR"); v != "" {
		cfg.noColor = true
	}
}

// parseGlobals reads the leading global flags from args and returns the rest. It
// stops at the first argument that is not a recognized flag, so a database path or
// a subcommand name ends the flag run.
func parseGlobals(args []string) (config, []string, error) {
	cfg := defaultConfig()
	cfg.applyEnv()

	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if a == "" || a[0] != '-' || a == "-" {
			break
		}
		name, val, hasVal := splitFlag(a)
		// next returns the inline value or consumes the following argument.
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", usagef("flag " + name + " needs a value")
			}
			i++
			return args[i], nil
		}
		switch name {
		case "-h", "-help", "--help":
			cfg.showHelp = true
		case "-v", "-version", "--version":
			cfg.showVersion = true
		case "-readonly", "-ro":
			cfg.readOnly = true
		case "-batch":
			cfg.batch = true
		case "-nocolor", "-no-color":
			cfg.noColor = true
		case "-mode", "-m":
			v, err := next()
			if err != nil {
				return cfg, nil, err
			}
			cfg.mode = v
		case "-init":
			v, err := next()
			if err != nil {
				return cfg, nil, err
			}
			cfg.initFile = v
		case "-null":
			v, err := next()
			if err != nil {
				return cfg, nil, err
			}
			cfg.nullText = v
		case "-noheader", "-noheaders":
			cfg.headers = false
		case "-timeout", "-busy-timeout":
			v, err := next()
			if err != nil {
				return cfg, nil, err
			}
			d, perr := parseTimeout(v)
			if perr != nil {
				return cfg, nil, usagef("bad -timeout value " + v)
			}
			cfg.busyTimeout = d
		default:
			return cfg, nil, usagef("unknown flag " + name)
		}
	}
	return cfg, args[i:], nil
}

// splitFlag splits "-name=value" into its parts. A flag without '=' has hasVal false.
func splitFlag(a string) (name, val string, hasVal bool) {
	if eq := strings.IndexByte(a, '='); eq >= 0 {
		return a[:eq], a[eq+1:], true
	}
	return a, "", false
}

// parseTimeout accepts a Go duration ("500ms", "5s") or a bare number of seconds.
func parseTimeout(v string) (time.Duration, error) {
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, err
	}
	return time.Duration(n) * time.Second, nil
}
