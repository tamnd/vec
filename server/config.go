// Package server turns the embedded vec library into a networked service
// (spec 16). It is the outermost surface over one engine: the same *vec.DB, the
// same transactions, the same ANN indexes, projected over the network for
// callers that cannot link the library in process.
//
// Three protocols share one open database. REST/JSON (net/http) is the portable
// path for any HTTP client. gRPC over HTTP/2 carries the binary proto3 messages
// for generated clients. The PostgreSQL wire protocol lets existing pgvector
// clients connect with no code change. All three run through one writer pipeline
// so the single-writer MVCC invariant holds across the wire.
//
// The implementation uses the standard library only. There is no grpc-go and no
// protobuf runtime; the proto3 codec and the PG wire framing are hand-written in
// the vecpb and pgwire subpackages.
package server

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved server configuration (spec 16 §10.2). A zero Config is
// not valid; build one with DefaultConfig and override fields, or parse flags and
// environment with ParseConfig.
type Config struct {
	// Listeners. An empty address disables that protocol.
	GRPCAddr    string // gRPC over HTTP/2, default 0.0.0.0:7700
	RESTAddr    string // REST/JSON over HTTP/1.1, default 0.0.0.0:7701
	PGAddr      string // PostgreSQL wire, disabled unless set
	MetricsAddr string // separate Prometheus endpoint, disabled unless set

	// Data. Exactly one of Path or DataDir names the engine file.
	Path     string // single .vec file, or :memory:
	DataDir  string // directory of .vec files for multi-file mode
	ReadOnly bool   // open the file read-only

	// Auth.
	AuthMode string  // token, jwt, or none
	Tokens   []Token // static tokens for AuthMode token

	// TLS.
	TLSCert string // server certificate file
	TLSKey  string // server private key file
	TLSCA   string // client CA for mTLS, optional

	// Limits (spec 16 §23).
	MaxConnections      int
	MaxWriteQueueDepth  int
	MaxUpsertBatch      int
	MaxQueryBatch       int
	DefaultQueryTimeout time.Duration
	SlowQueryThreshold  time.Duration
	ShutdownGrace       time.Duration
	BusyTimeout         time.Duration

	// Logging.
	LogLevel string // debug, info, warn, error
}

// Token is one static API token and the access it grants (spec 16 §6.1, §7.1).
type Token struct {
	ID          string   // human label for logs and rotation
	Secret      string   // the bearer token value
	Role        string   // admin, readwrite, or reader
	Collections []string // allowed collections, empty means all
}

// DefaultConfig returns the built-in defaults (spec 16 §23) before flags and
// environment are applied.
func DefaultConfig() Config {
	return Config{
		GRPCAddr:            "0.0.0.0:7700",
		RESTAddr:            "0.0.0.0:7701",
		Path:                ":memory:",
		AuthMode:            "token",
		MaxConnections:      1000,
		MaxWriteQueueDepth:  512,
		MaxUpsertBatch:      10000,
		MaxQueryBatch:       256,
		DefaultQueryTimeout: 30 * time.Second,
		SlowQueryThreshold:  100 * time.Millisecond,
		ShutdownGrace:       30 * time.Second,
		BusyTimeout:         5 * time.Second,
		LogLevel:            "info",
	}
}

// ParseConfig builds a Config from the serve subcommand arguments. Precedence is
// flag over environment over default (spec 16 §10.1). The first non-flag argument,
// if present, is the database path.
func ParseConfig(args []string) (Config, error) {
	cfg := DefaultConfig()
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
		name, val, hasVal := splitArg(a)
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", name)
			}
			i++
			return args[i], nil
		}
		var err error
		switch name {
		case "-grpc", "-grpc-addr":
			cfg.GRPCAddr, err = next()
		case "-rest", "-rest-addr", "-http", "-addr":
			cfg.RESTAddr, err = next()
		case "-pg", "-pg-addr":
			cfg.PGAddr, err = next()
		case "-metrics", "-metrics-addr":
			cfg.MetricsAddr, err = next()
		case "-data-dir":
			cfg.DataDir, err = next()
		case "-readonly", "-ro":
			cfg.ReadOnly = true
		case "-auth", "-auth-mode":
			cfg.AuthMode, err = next()
		case "-no-auth":
			cfg.AuthMode = "none"
		case "-token":
			var v string
			if v, err = next(); err == nil {
				cfg.Tokens = append(cfg.Tokens, parseTokenSpec(v))
			}
		case "-tls-cert":
			cfg.TLSCert, err = next()
		case "-tls-key":
			cfg.TLSKey, err = next()
		case "-tls-ca":
			cfg.TLSCA, err = next()
		case "-log-level":
			cfg.LogLevel, err = next()
		case "-timeout", "-busy-timeout":
			var v string
			if v, err = next(); err == nil {
				cfg.BusyTimeout, err = parseDuration(v)
			}
		default:
			return cfg, fmt.Errorf("unknown serve flag %s", name)
		}
		if err != nil {
			return cfg, err
		}
	}
	if rest := args[i:]; len(rest) > 0 {
		cfg.Path = rest[0]
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// applyEnv folds VEC_* environment variables into cfg. Flags override these.
func (cfg *Config) applyEnv() {
	if v := os.Getenv("VEC_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	if v := os.Getenv("VEC_REST_ADDR"); v != "" {
		cfg.RESTAddr = v
	}
	if v := os.Getenv("VEC_PG_ADDR"); v != "" {
		cfg.PGAddr = v
	}
	if v := os.Getenv("VEC_METRICS_ADDR"); v != "" {
		cfg.MetricsAddr = v
	}
	if v := os.Getenv("VEC_DATABASE"); v != "" {
		cfg.Path = v
	}
	if v := os.Getenv("VEC_AUTH_MODE"); v != "" {
		cfg.AuthMode = v
	}
	if v := os.Getenv("VEC_TOKEN"); v != "" {
		cfg.Tokens = append(cfg.Tokens, parseTokenSpec(v))
	}
	if v := os.Getenv("VEC_TLS_CERT"); v != "" {
		cfg.TLSCert = v
	}
	if v := os.Getenv("VEC_TLS_KEY"); v != "" {
		cfg.TLSKey = v
	}
	if v := os.Getenv("VEC_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
}

// validate rejects a configuration that cannot start.
func (cfg *Config) validate() error {
	if cfg.GRPCAddr == "" && cfg.RESTAddr == "" && cfg.PGAddr == "" {
		return fmt.Errorf("no listeners configured: set at least one of grpc, rest, or pg address")
	}
	switch cfg.AuthMode {
	case "token", "jwt", "none":
	default:
		return fmt.Errorf("unknown auth mode %q", cfg.AuthMode)
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return fmt.Errorf("tls cert and key must be set together")
	}
	return nil
}

// TLSEnabled reports whether a certificate and key were configured.
func (cfg *Config) TLSEnabled() bool { return cfg.TLSCert != "" && cfg.TLSKey != "" }

// parseTokenSpec reads a token spec of the form "secret[:role[:coll1,coll2]]".
func parseTokenSpec(s string) Token {
	parts := strings.SplitN(s, ":", 3)
	t := Token{Secret: parts[0], Role: "admin"}
	if len(parts) > 1 && parts[1] != "" {
		t.Role = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		t.Collections = strings.Split(parts[2], ",")
	}
	t.ID = "token-" + shortHash(t.Secret)
	return t
}

// splitArg splits "-name=value" into its parts. A flag without '=' has hasVal false.
func splitArg(a string) (name, val string, hasVal bool) {
	if eq := strings.IndexByte(a, '='); eq >= 0 {
		return a[:eq], a[eq+1:], true
	}
	return a, "", false
}

// parseDuration accepts a Go duration or a bare number of seconds.
func parseDuration(v string) (time.Duration, error) {
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q", v)
	}
	return time.Duration(n) * time.Second, nil
}
