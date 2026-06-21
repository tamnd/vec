package vec

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/tamnd/vec/config"
	"github.com/tamnd/vec/vectorsql"
)

// This file implements the configuration surface from spec 22: the PRAGMA
// command, programmatic Options derived from a knob map or the environment, the
// effective-config snapshot, and the typed read helpers. The knob catalogue
// itself lives in the config package; this file binds it to a live *DB.
//
// The engine in this build is process-resident, so persistent-runtime PRAGMAs
// are held in an in-memory store rather than a catalog page on disk. The store
// layers exactly as spec 22 §1.4 describes (session over persistent over the
// open-time config over the compiled-in default), so the precedence a caller
// observes is the shipped behavior; only the on-disk persistence waits on the
// pager wiring that the rest of the engine waits on too.

// Pragma reads or sets a database knob by name (spec 22 §19). An empty value
// reads the current effective value; a non-empty value sets it and returns the
// canonical stored form. Reads of diagnostic and configuration PRAGMAs are also
// handled here. Unknown names return *ErrUnknownPragma; read-only and
// create-time knobs reject writes with *ErrPragmaReadOnly and *ErrPragmaImmutable.
func (db *DB) Pragma(ctx context.Context, name, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", ctxErr(err)
	}
	db.mu.RLock()
	closed := db.closed
	db.mu.RUnlock()
	if closed {
		return "", ErrClosed
	}

	lname := strings.ToLower(strings.TrimSpace(name))
	if r, handled, err := db.specialPragma(lname, value); handled {
		return r, err
	}
	if sqliteCompatNoop[lname] {
		return "", nil
	}

	knob, ok := config.Lookup(lname)
	if !ok {
		return "", &ErrUnknownPragma{Pragma: name}
	}
	if value == "" {
		return db.pragmaRead(knob), nil
	}
	return db.pragmaSet(knob, value)
}

// pragmaRead resolves the effective value of knob using the precedence stack.
func (db *DB) pragmaRead(knob *config.Knob) string {
	db.pragmaMu.Lock()
	defer db.pragmaMu.Unlock()
	if v, ok := db.session[knob.Name]; ok {
		return v
	}
	if v, ok := db.persistent[knob.Name]; ok {
		return v
	}
	if v, ok := db.liveValue(knob.Name); ok {
		return v
	}
	if knob.Computed {
		return db.computedDefault(knob)
	}
	return knob.Default
}

// pragmaSet validates value against knob, stores it in the right tier, and
// returns the canonical form.
func (db *DB) pragmaSet(knob *config.Knob, value string) (string, error) {
	if knob.ReadOnly {
		return "", &ErrPragmaReadOnly{Pragma: knob.Name}
	}
	if knob.Tier == config.TierCreate {
		return "", &ErrPragmaImmutable{
			Pragma:    knob.Name,
			FileValue: db.pragmaRead(knob),
			NewValue:  value,
		}
	}
	canon, err := knob.Canonicalize(value)
	if err != nil {
		var ve *config.ValueError
		if errors.As(err, &ve) {
			return "", &ErrInvalidConfig{Knob: knob.Name, Value: value, Reason: ve.Reason}
		}
		return "", err
	}
	db.pragmaMu.Lock()
	defer db.pragmaMu.Unlock()
	if knob.Tier == config.TierSession {
		db.session[knob.Name] = canon
	} else {
		db.persistent[knob.Name] = canon
	}
	return canon, nil
}

// liveValue returns the value of a knob that backs a live open-time config field,
// so a read reflects the engine's actual state rather than the registry default.
func (db *DB) liveValue(name string) (string, bool) {
	switch name {
	case "page_size":
		return strconv.Itoa(db.cfg.pageSize), true
	case "cache_size":
		return strconv.FormatInt(db.cfg.cacheBytes, 10), true
	case "synchronous":
		return strings.ToUpper(db.cfg.sync.String()), true
	case "mmap_size":
		if db.cfg.mmap {
			return strconv.FormatInt(db.cfg.cacheBytes, 10), true
		}
		return "0", true
	case "worker_pool_size", "hnsw_build_threads", "gomaxprocs":
		if db.cfg.parallelism > 0 {
			return strconv.Itoa(db.cfg.parallelism), true
		}
	}
	return "", false
}

// computedDefault resolves an environment-derived default to a concrete value at
// read time. Knobs whose default depends on collection geometry (ivf_nlist,
// pq_m, rerank_r) cannot be resolved without a query, so they return the
// descriptive default unchanged.
func (db *DB) computedDefault(knob *config.Knob) string {
	switch knob.Name {
	case "gomaxprocs":
		return strconv.Itoa(runtime.GOMAXPROCS(0))
	case "hnsw_build_threads", "worker_pool_size":
		return strconv.Itoa(runtime.GOMAXPROCS(0))
	case "hnsw_max_m0":
		return "32"
	case "btree_max_inline_value":
		return strconv.Itoa(db.cfg.pageSize / 4)
	case "cache_size":
		return strconv.FormatInt(db.cfg.cacheBytes, 10)
	default:
		return knob.Default
	}
}

// PragmaInt reads a knob as an int64. It is the typed counterpart of Pragma for
// integer knobs (spec 22 §19.5).
func (db *DB) PragmaInt(ctx context.Context, name string) (int64, error) {
	s, err := db.Pragma(ctx, name, "")
	if err != nil {
		return 0, err
	}
	n, err := config.ParseSize(s)
	if err != nil {
		return 0, fmt.Errorf("vec: pragma %s is not an integer (%q)", name, s)
	}
	return n, nil
}

// PragmaString reads a knob as its string form (spec 22 §19.5).
func (db *DB) PragmaString(ctx context.Context, name string) (string, error) {
	return db.Pragma(ctx, name, "")
}

// PragmaInt is the package-level helper shown in spec 22 §19.5; it reads with a
// background context.
func PragmaInt(db *DB, name string) (int64, error) {
	return db.PragmaInt(context.Background(), name)
}

// PragmaString is the package-level string helper (spec 22 §19.5).
func PragmaString(db *DB, name string) (string, error) {
	return db.PragmaString(context.Background(), name)
}

// execPragma runs a PRAGMA statement parsed from VectorSQL and returns a cursor
// with the single column "value" and one row (spec 22 §19.5). The read form
// (Value nil) reads; the set forms (= value, (value)) set and echo the canonical
// value back.
func (db *DB) execPragma(ctx context.Context, pr *vectorsql.PragmaStmt) (*Rows, error) {
	value := ""
	if pr.Value != nil {
		v, err := pragmaLiteral(pr.Value)
		if err != nil {
			return nil, err
		}
		value = v
	}
	out, err := db.Pragma(ctx, pr.Name, value)
	if err != nil {
		return nil, err
	}
	return singleValueRows(out), nil
}

// pragmaLiteral renders a PRAGMA value expression as its source string. PRAGMA
// values are literals or bare identifiers (spec 22 §19.1), not general
// expressions, so this covers the grammar.
func pragmaLiteral(e vectorsql.Expr) (string, error) {
	switch v := e.(type) {
	case *vectorsql.IntLit:
		return strconv.FormatInt(v.Value, 10), nil
	case *vectorsql.FloatLit:
		return strconv.FormatFloat(v.Value, 'g', -1, 64), nil
	case *vectorsql.StringLit:
		return v.Value, nil
	case *vectorsql.BoolLit:
		if v.Value {
			return "on", nil
		}
		return "off", nil
	case *vectorsql.ColumnRef:
		return v.Name, nil
	default:
		return "", fmt.Errorf("vec: pragma value must be a literal: %w", ErrSchemaViolation)
	}
}

// singleValueRows builds a one-row, one-column ("value") cursor for a PRAGMA
// read or echo.
func singleValueRows(value string) *Rows {
	r := Result{columns: map[string]Value{"value": TextValue(value)}}
	return &Rows{results: []Result{r}}
}

// sqliteCompatNoop lists SQLite PRAGMAs that have no vec meaning. They return an
// empty result rather than an error so tools that enumerate SQLite PRAGMAs do not
// crash on a vec database (spec 22 §28.2).
var sqliteCompatNoop = map[string]bool{
	"collation_list":  true,
	"compile_options": true,
	"foreign_keys":    true,
	"table_list":      true,
}
