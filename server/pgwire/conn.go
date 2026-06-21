package pgwire

import (
	"bufio"
	"context"
	"net"
	"sync"

	vec "github.com/tamnd/vec"
)

// Options configures the PG wire server (spec 16 §4.3, §6.1).
type Options struct {
	// DB is the engine the wire layer projects. Required.
	DB *vec.DB
	// Version is the vec version string folded into server_version (spec 16 §4.3).
	Version string
	// AuthMode is "none"/"trust" (AuthenticationOk immediately) or "password"
	// (AuthenticationCleartextPassword, verified via Verify), spec 16 §4.3.
	AuthMode string
	// Verify checks a user/password pair when AuthMode is "password". A nil
	// Verify with password mode rejects every connection.
	Verify func(user, password string) error
	// Logger logs connection-level events; nil disables logging.
	Logger func(format string, args ...any)
}

// prepared is a parsed statement held in the per-connection statement cache
// (spec 16 §5.3). It keeps the SQL text and the inferred parameter type OIDs.
type prepared struct {
	sql       string
	paramOIDs []int32
}

// portal is a bound statement ready to execute (spec 16 §5.3). It carries the
// SQL with parameter values substituted and the requested result formats.
type portal struct {
	stmt          *prepared
	sql           string  // SQL with $N parameters substituted as literals
	resultFormats []int16 // per-column format codes (0=text, 1=binary)
}

// txnState is the connection transaction state (spec 16 §5.2). PG wire is always
// one connection per session, so a transaction is naturally connection-scoped.
type txnState byte

const (
	txnIdle    txnState = 'I' // not in a transaction block
	txnInBlock txnState = 'T' // inside BEGIN, not yet committed
	txnFailed  txnState = 'E' // inside a failed transaction block
)

// Conn wraps a net.Conn with buffered IO and the per-connection session state
// (spec 16 §5.1). One Conn serves one TCP connection.
type Conn struct {
	raw  net.Conn
	r    *msgReader
	w    *msgWriter
	opts Options

	// user is the authenticated role from the startup message.
	user string
	// database is the requested database parameter (spec 16 §4.3).
	database string

	// txn holds the open interactive transaction, nil outside a BEGIN block.
	txn   *vec.Txn
	state txnState

	// stmts and portals are the per-connection caches (spec 16 §5.3).
	stmts   map[string]*prepared
	portals map[string]*portal

	// session holds SET name=value overrides applied this connection (spec 16 §4.6).
	session map[string]string
}

// newConn wraps c with buffered IO and an empty session.
func newConn(c net.Conn, opts Options) *Conn {
	return &Conn{
		raw:     c,
		r:       &msgReader{r: bufio.NewReader(c)},
		w:       &msgWriter{w: bufio.NewWriter(c)},
		opts:    opts,
		state:   txnIdle,
		stmts:   make(map[string]*prepared),
		portals: make(map[string]*portal),
		session: make(map[string]string),
	}
}

// logf logs through the configured logger if present.
func (c *Conn) logf(format string, args ...any) {
	if c.opts.Logger != nil {
		c.opts.Logger(format, args...)
	}
}

// closeTxn rolls back any open transaction; used on disconnect and on errors.
func (c *Conn) closeTxn() {
	if c.txn != nil {
		_ = c.txn.Rollback()
		c.txn = nil
	}
	c.state = txnIdle
}

// Serve runs the PG wire accept loop until ctx is canceled or ln is closed
// (spec 16 §22.2). Each accepted connection runs the protocol loop in its own
// goroutine; the loop honors ctx for graceful drain.
func Serve(ctx context.Context, ln net.Listener, opts Options) error {
	if opts.DB == nil {
		return errNoDB
	}
	if opts.Version == "" {
		opts.Version = vec.Version()
	}

	// Close the listener when ctx is canceled so Accept unblocks (spec 16 §22.2).
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := newConn(nc, opts)
			c.serve(ctx)
		}()
	}
}
