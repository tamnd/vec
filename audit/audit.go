// Package audit implements the security audit log from spec 23 section 11: an
// append-only NDJSON record of who did what to which collection at what time. It
// is separate from the query log (spec 18), which captures performance and query
// shapes; this log captures security-relevant events only. The package is
// standalone; the server composes it into its request pipeline, and the embedded
// library leaves it disabled by default.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Event categories from spec 23 section 11.1. The values are the dotted event
// names that appear in the "event" field of each line.
const (
	EventAuthLogin     = "auth.login"
	EventAuthFail      = "auth.fail"
	EventAuthExpired   = "auth.expired"
	EventAuthDeny      = "authz.deny"
	EventDataInsert    = "data.insert"
	EventDataUpsert    = "data.upsert"
	EventDataDelete    = "data.delete"
	EventSchemaChange  = "schema.change"
	EventKeyChangePass = "key.change_passphrase"
	EventKeyRotateDEK  = "key.rotate_dek"
	EventKeyRekey      = "key.rekey_vacuum"
	EventAdminOp       = "admin.op"
	EventServerStart   = "server.start"
	EventServerStop    = "server.stop"
	EventConfigReload  = "server.config_reload"
)

// Event is one audit record. Only set fields are written; empty fields are
// omitted so the line stays compact and the schema stays open for new event
// types (spec 23 section 11.2). Ts and Level are filled by the Logger.
type Event struct {
	Ts         string `json:"ts"`
	Level      string `json:"level"`
	Event      string `json:"event"`
	Principal  string `json:"principal,omitempty"`
	Collection string `json:"collection,omitempty"`
	Op         string `json:"op,omitempty"`
	Count      int64  `json:"count,omitempty"`
	Filter     string `json:"filter,omitempty"`
	LSN        uint64 `json:"lsn,omitempty"`
	DurationUS int64  `json:"duration_us,omitempty"`
	Reason     string `json:"reason,omitempty"`
	OldEpoch   uint16 `json:"old_epoch,omitempty"`
	NewEpoch   uint16 `json:"new_epoch,omitempty"`
	Pages      uint64 `json:"pages,omitempty"`
	Version    string `json:"version,omitempty"`
	ConfigHash string `json:"config_hash,omitempty"`
	OK         bool   `json:"ok,omitempty"`
}

// Clock supplies the timestamp for an event. It is an injection point so tests
// get deterministic output; the server passes time.Now.
type Clock func() time.Time

// Logger serializes events to an append-only sink as NDJSON. Writes are
// serialized by a mutex so concurrent request handlers do not interleave bytes
// within a line (spec 23 section 11.2: each line contains no embedded newlines
// and is a complete JSON object).
type Logger struct {
	mu    sync.Mutex
	w     io.Writer
	clock Clock
	close func() error
}

// New builds a Logger over an arbitrary writer. The caller owns the writer's
// lifecycle; Close is a no-op on this path.
func New(w io.Writer, clock Clock) *Logger {
	if clock == nil {
		clock = time.Now
	}
	return &Logger{w: w, clock: clock, close: func() error { return nil }}
}

// Open opens the audit log file with O_WRONLY|O_APPEND|O_CREATE and mode 0600
// (spec 23 section 11.3 items 1 and 2: an append-only descriptor a process
// cannot seek backwards through, readable and writable only by the owner). The
// returned Logger owns the file and closes it in Close.
func Open(path string, clock Clock) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vec/audit: open %s: %w", path, err)
	}
	if clock == nil {
		clock = time.Now
	}
	return &Logger{w: f, clock: clock, close: f.Close}, nil
}

// Log writes one event. It stamps the timestamp in RFC 3339 with microseconds
// (the format in the spec example) and the fixed AUDIT level, then writes the
// JSON object followed by a single newline.
func (l *Logger) Log(e Event) error {
	e.Ts = l.clock().UTC().Format("2006-01-02T15:04:05.000000Z")
	e.Level = "AUDIT"
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("vec/audit: marshal event: %w", err)
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(line); err != nil {
		return fmt.Errorf("vec/audit: write event: %w", err)
	}
	return nil
}

// Close releases the underlying file if Open created one.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.close()
}

// Discard is a Logger that drops every event. It is the default for the embedded
// library and the CLI, which have no audit requirement, so a nil-free call site
// can always log without a configuration branch.
func Discard() *Logger {
	return New(io.Discard, time.Now)
}
