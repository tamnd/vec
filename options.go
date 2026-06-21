package vec

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/tamnd/vec/crypto"
)

// SyncLevel controls how aggressively the WAL is flushed to stable storage on
// commit (spec 14 §2.3, mirroring the WAL sync levels of spec 05).
type SyncLevel int

const (
	SyncOff    SyncLevel = iota // never fsync; fastest, least durable
	SyncNormal                  // fsync at checkpoints (the default)
	SyncFull                    // fsync at every commit
	SyncExtra                   // fsync commit and the directory entry
)

// String renders the sync level as the pragma keyword used by Pragma and the CLI.
func (s SyncLevel) String() string {
	switch s {
	case SyncOff:
		return "off"
	case SyncNormal:
		return "normal"
	case SyncFull:
		return "full"
	case SyncExtra:
		return "extra"
	default:
		return "normal"
	}
}

// Option configures a database at open time (spec 14 §2.2). Options are applied
// in order; a later option overrides an earlier one.
type Option func(*openConfig)

// openConfig is the resolved configuration an Open call assembles from its
// options. It is unexported; callers only see the Option functions.
type openConfig struct {
	pageSize        int
	cacheBytes      int64
	mmap            bool
	sync            SyncLevel
	busyTimeout     time.Duration
	readOnly        bool
	createIfMissing bool
	parallelism     int
	maxRetries      int
	logger          Logger
	tracer          Tracer
	metrics         MetricSink
	progress        func(IndexBuildStats)

	// encryption inputs (spec 23 §3). At most one of encPassphrase / encKey is
	// set; encCipher selects the page cipher and defaults to AES-256-GCM.
	encPassphrase Passphrase
	encKey        EncryptionKey
	encCipher     crypto.Cipher
}

// defaultConfig returns the baseline configuration before options are applied
// (spec 14 §2.4).
func defaultConfig() openConfig {
	return openConfig{
		pageSize:        4096,
		cacheBytes:      64 << 20,
		mmap:            true,
		sync:            SyncNormal,
		busyTimeout:     5 * time.Second,
		createIfMissing: true,
		parallelism:     runtime.NumCPU(),
		maxRetries:      10,
		logger:          DefaultLogger,
	}
}

// WithPageSize sets the database page size in bytes; it must be a power of two
// and is fixed for the life of the file (spec 14 §2.2).
func WithPageSize(bytes int) Option { return func(c *openConfig) { c.pageSize = bytes } }

// WithCreateIfMissing controls whether Open creates the file when it is absent.
func WithCreateIfMissing(v bool) Option { return func(c *openConfig) { c.createIfMissing = v } }

// WithCacheSize sets the buffer-pool budget in bytes.
func WithCacheSize(bytes int64) Option { return func(c *openConfig) { c.cacheBytes = bytes } }

// WithMMap enables or disables memory-mapped reads of the data file.
func WithMMap(on bool) Option { return func(c *openConfig) { c.mmap = on } }

// WithSynchronous sets the WAL sync level.
func WithSynchronous(level SyncLevel) Option { return func(c *openConfig) { c.sync = level } }

// WithBusyTimeout sets how long Begin waits for the write lock before ErrBusy.
func WithBusyTimeout(d time.Duration) Option { return func(c *openConfig) { c.busyTimeout = d } }

// WithReadOnly opens the database for reading only.
func WithReadOnly(v bool) Option { return func(c *openConfig) { c.readOnly = v } }

// WithParallelism sets the goroutine count for index builds and batch upserts.
func WithParallelism(n int) Option { return func(c *openConfig) { c.parallelism = n } }

// WithMaxRetries caps the conflict-retry count for Update.
func WithMaxRetries(n int) Option { return func(c *openConfig) { c.maxRetries = n } }

// WithLogger routes internal log events to l.
func WithLogger(l Logger) Option { return func(c *openConfig) { c.logger = l } }

// WithTracer routes span events to t.
func WithTracer(t Tracer) Option { return func(c *openConfig) { c.tracer = t } }

// WithMetrics routes metric observations to m.
func WithMetrics(m MetricSink) Option { return func(c *openConfig) { c.metrics = m } }

// WithProgress registers an index-build progress callback.
func WithProgress(fn func(IndexBuildStats)) Option {
	return func(c *openConfig) { c.progress = fn }
}

// WithPassphrase enables at-rest encryption with a passphrase (spec 23 §3). The
// master key is derived with Argon2id; opening the same database later needs the
// same passphrase, and a wrong one fails with ErrWrongPassphrase before any data
// page is read.
func WithPassphrase(p Passphrase) Option {
	return func(c *openConfig) { c.encPassphrase = p; c.encKey = nil }
}

// WithEncryptionKey enables at-rest encryption with a caller-supplied 32-byte raw
// key (spec 23 §3), for deployments that manage keys in an external KMS or HSM and
// inject the key at open time. The key is used as the master key directly with no
// passphrase KDF.
func WithEncryptionKey(key EncryptionKey) Option {
	return func(c *openConfig) { c.encKey = key; c.encPassphrase = "" }
}

// WithCipher selects the page cipher for a newly created encrypted database (spec
// 23 §2.2). The default is AES-256-GCM; ChaCha20-Poly1305 is the choice for hosts
// without AES hardware acceleration. The setting is ignored when opening an
// existing encrypted database, which uses the cipher recorded in its header.
func WithCipher(c crypto.Cipher) Option {
	return func(cfg *openConfig) { cfg.encCipher = c }
}

// Logger receives internal log events (spec 14 §12.2). Implement it to route
// vec's logs into slog, zap, logrus, or any other framework.
type Logger interface {
	// Log is called with a level ("DEBUG", "INFO", "WARN", "ERROR"), a message,
	// and key-value pairs.
	Log(level, msg string, kvs ...any)
}

// Tracer receives span events for distributed tracing (spec 14 §12.3). vec
// creates one span per query, transaction, and index build.
type Tracer interface {
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// Span is one tracing span.
type Span interface {
	SetAttribute(key string, value any)
	End()
	RecordError(err error)
}

// MetricSink receives metric observations (spec 14 §12.4).
type MetricSink interface {
	Counter(name string, delta int64, tags ...string)
	Gauge(name string, value float64, tags ...string)
	Histogram(name string, value float64, tags ...string)
}

// LoadFunc is the callback signature for BulkLoad (spec 14 §8).
type LoadFunc func(ctx context.Context, bw *BulkWriter) error

// stderrLogger is the default Logger; it writes structured lines to stderr.
type stderrLogger struct{}

func (stderrLogger) Log(level, msg string, kvs ...any) {
	if len(kvs) == 0 {
		fmt.Fprintf(os.Stderr, "vec %-5s %s\n", level, msg)
		return
	}
	fmt.Fprintf(os.Stderr, "vec %-5s %s %v\n", level, msg, kvs)
}

// DefaultLogger writes to stderr in a structured format (spec 14 §12.2).
var DefaultLogger Logger = stderrLogger{}
