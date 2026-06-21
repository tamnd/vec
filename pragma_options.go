package vec

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/tamnd/vec/config"
)

// appendErr keeps the first option error so Open reports a stable cause.
func appendErr(existing, err error) error {
	if existing != nil {
		return existing
	}
	return err
}

// asValueErr is errors.As specialized to config.ValueError.
func asValueErr(err error, target **config.ValueError) bool {
	return errors.As(err, target)
}

// WithPragma sets one knob through the Option surface (spec 22 §26.1). The value
// is validated and canonicalized when Open applies the option; an invalid value
// surfaces as an *ErrInvalidConfig from Open.
func WithPragma(name, value string) Option {
	return func(c *openConfig) {
		knob, ok := config.Lookup(name)
		if !ok {
			c.pragmaErr = appendErr(c.pragmaErr, &ErrUnknownPragma{Pragma: name})
			return
		}
		canon, err := knob.Canonicalize(value)
		if err != nil {
			var ve *config.ValueError
			if asValueErr(err, &ve) {
				c.pragmaErr = appendErr(c.pragmaErr, &ErrInvalidConfig{Knob: knob.Name, Value: value, Reason: ve.Reason})
			} else {
				c.pragmaErr = appendErr(c.pragmaErr, err)
			}
			return
		}
		c.setPragma(knob.Name, canon)
	}
}

// ParseOptions converts a map of PRAGMA name to value into a slice of Options
// (spec 22 §26.2). It is the bridge for opening a database from a DSN query
// string, an environment, or any external key-value source. An unknown name or
// an invalid value returns an error and no options.
func ParseOptions(pragmas map[string]string) ([]Option, error) {
	names := make([]string, 0, len(pragmas))
	for name := range pragmas {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic application order
	opts := make([]Option, 0, len(names))
	for _, name := range names {
		knob, ok := config.Lookup(name)
		if !ok {
			return nil, &ErrUnknownPragma{Pragma: name}
		}
		canon, err := knob.Canonicalize(pragmas[name])
		if err != nil {
			var ve *config.ValueError
			if asValueErr(err, &ve) {
				return nil, &ErrInvalidConfig{Knob: knob.Name, Value: pragmas[name], Reason: ve.Reason}
			}
			return nil, err
		}
		canonical := canon
		canonicalName := knob.Name
		opts = append(opts, func(c *openConfig) { c.setPragma(canonicalName, canonical) })
	}
	return opts, nil
}

// OptionsFromEnv reads VEC_ environment variables from environ (each "KEY=value")
// and turns the database-level ones into Options (spec 22 §22.2). The variable
// name is the knob name uppercased with VEC_ prepended and dots as underscores;
// the section prefixes (SERVER_, DATABASE_, etc.) are stripped so VEC_EF_SEARCH
// and VEC_DATABASE_CACHE_SIZE both resolve. Names that match no knob are skipped.
func OptionsFromEnv(environ []string) ([]Option, error) {
	pragmas := map[string]string{}
	for _, kv := range environ {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		name, ok := envKnobName(key)
		if !ok {
			continue
		}
		pragmas[name] = val
	}
	return ParseOptions(pragmas)
}

// envKnobName maps a VEC_ environment variable to a knob name, or reports false
// when the variable is not a VEC_ knob.
func envKnobName(key string) (string, bool) {
	up := strings.ToUpper(strings.TrimSpace(key))
	if !strings.HasPrefix(up, "VEC_") {
		return "", false
	}
	rest := strings.ToLower(up[len("VEC_"):])
	if _, ok := config.Lookup(rest); ok {
		return rest, true
	}
	for _, section := range []string{"database_", "server_", "observability_"} {
		if strings.HasPrefix(rest, section) {
			trimmed := strings.TrimPrefix(rest, section)
			if _, ok := config.Lookup(trimmed); ok {
				return trimmed, true
			}
		}
	}
	return "", false
}

// applyLiveKnob reflects the handful of option-set knobs that back a live
// open-time config field, so the engine state and the config snapshot match the
// requested value without waiting for a read through the knob store.
func applyLiveKnob(c *openConfig, name, canonical string) {
	switch name {
	case "page_size":
		if n, err := strconv.Atoi(canonical); err == nil {
			c.pageSize = n
		}
	case "cache_size":
		if n, err := config.ParseSize(canonical); err == nil {
			if n < 0 {
				c.cacheBytes = -n // negative = bytes (spec 22 §3.2)
			} else if c.pageSize > 0 {
				c.cacheBytes = n * int64(c.pageSize)
			}
		}
	case "mmap_size":
		if n, err := config.ParseSize(canonical); err == nil {
			c.mmap = n > 0
		}
	case "synchronous":
		switch strings.ToUpper(canonical) {
		case "OFF":
			c.sync = SyncOff
		case "NORMAL":
			c.sync = SyncNormal
		case "FULL":
			c.sync = SyncFull
		case "EXTRA":
			c.sync = SyncExtra
		}
	case "gomaxprocs", "worker_pool_size":
		if n, err := strconv.Atoi(canonical); err == nil && n > 0 {
			c.parallelism = n
		}
	}
}

// validateConfig runs the cross-knob checks from spec 22 §26.4 against the
// resolved configuration. It runs at Open before any data is touched.
func (db *DB) validateConfig() error {
	if db.cfg.pragmaErr != nil {
		return db.cfg.pragmaErr
	}
	get := func(name string) string {
		k, ok := config.Lookup(name)
		if !ok {
			return ""
		}
		return db.pragmaRead(k)
	}
	gf := func(name string) (float64, bool) {
		f, err := strconv.ParseFloat(get(name), 64)
		return f, err == nil
	}
	gi := func(name string) (int64, bool) {
		n, err := config.ParseSize(get(name))
		return n, err == nil
	}

	graph, gok := gf("graph_pool_fraction")
	seg, sok := gf("segment_pool_fraction")
	if gok && sok && graph+seg > 1.0 {
		return &ErrInvalidConfig{Knob: "graph_pool_fraction", Value: graph + seg, Reason: "pool fractions sum to more than 1.0"}
	}
	m, mok := gi("hnsw_m")
	if efc, ok := gi("hnsw_ef_construction"); ok && mok && efc < m {
		return &ErrInvalidConfig{Knob: "hnsw_ef_construction", Value: efc, Reason: "must be at least hnsw_m"}
	}
	if maxm0, ok := gi("hnsw_max_m0"); ok && mok && maxm0 < m {
		return &ErrInvalidConfig{Knob: "hnsw_max_m0", Value: maxm0, Reason: "must be at least hnsw_m"}
	}
	if db.cfg.cacheBytes > 0 && db.cfg.pageSize > 0 {
		if db.cfg.cacheBytes < int64(64*db.cfg.pageSize) {
			return &ErrInvalidConfig{Knob: "cache_size", Value: db.cfg.cacheBytes, Reason: "must be at least 64 pages"}
		}
	}
	cert, key := get("tls_cert"), get("tls_key")
	if (cert == "") != (key == "") {
		return &ErrInvalidConfig{Knob: "tls_cert", Value: cert, Reason: "tls_cert and tls_key must be set together"}
	}
	if get("auth_mode") == "mtls" && get("tls_ca") == "" {
		return &ErrInvalidConfig{Knob: "auth_mode", Value: "mtls", Reason: "mtls requires tls_ca"}
	}
	if rt, ok := gf("recall_target"); ok && (rt < 0 || rt > 1) {
		return &ErrInvalidConfig{Knob: "recall_target", Value: rt, Reason: "must be in [0, 1]"}
	}
	return nil
}

// Config is a read-only snapshot of the effective configuration at Open time plus
// any Options applied (spec 22 §26.3). It does not update when a PRAGMA is set
// after Open; use db.PragmaInt/PragmaString for the current stored value.
type Config struct {
	PageSize    int
	CacheBytes  int64
	Synchronous string
	MMap        bool
	Session     SessionConfig
}

// SessionConfig holds the session-tier defaults a connection starts with.
type SessionConfig struct {
	EfSearch int
	NProbe   int
	RerankR  int
}

// CacheSizeBytes returns the cache budget in bytes (spec 22 §26.3).
func (c Config) CacheSizeBytes() int64 { return c.CacheBytes }

// Config returns the effective configuration snapshot (spec 22 §26.3).
func (db *DB) Config() Config {
	ri := func(name string) int {
		k, ok := config.Lookup(name)
		if !ok {
			return 0
		}
		n, _ := config.ParseSize(db.pragmaRead(k))
		return int(n)
	}
	return Config{
		PageSize:    db.cfg.pageSize,
		CacheBytes:  db.cfg.cacheBytes,
		Synchronous: strings.ToUpper(db.cfg.sync.String()),
		MMap:        db.cfg.mmap,
		Session: SessionConfig{
			EfSearch: ri("hnsw_ef_search"),
			NProbe:   ri("ivf_nprobe"),
			RerankR:  ri("rerank_r"),
		},
	}
}
