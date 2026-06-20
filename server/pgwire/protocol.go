package pgwire

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

// errNoDB reports a Serve call with no engine.
var errNoDB = errors.New("pgwire: Options.DB is required")

// serve runs the full protocol for one connection: startup, auth, parameter
// status, then the query loop, until the client terminates or IO fails
// (spec 16 §4.2).
func (c *Conn) serve(ctx context.Context) {
	defer func() { _ = c.raw.Close() }()
	defer c.closeTxn()

	ok, err := c.startup(ctx)
	if err != nil {
		c.logf("pgwire: startup failed: %v", err)
		return
	}
	if !ok {
		// Startup handled an SSL/cancel request and the caller should not
		// continue into the query loop on this connection.
		return
	}

	if err := c.loop(ctx); err != nil && err != io.EOF {
		c.logf("pgwire: connection closed: %v", err)
	}
}

// startup reads the StartupMessage, handles SSL/cancel negotiation, runs auth,
// and sends the parameter status set, BackendKeyData, and ReadyForQuery
// (spec 16 §4.3, §17.1). It returns ok=false when the connection was a one-shot
// SSL or cancel request that should not continue.
func (c *Conn) startup(ctx context.Context) (bool, error) {
	for {
		body, err := c.r.readStartup()
		if err != nil {
			return false, err
		}
		if len(body) < 4 {
			return false, errShortRead
		}
		code := binary.BigEndian.Uint32(body[:4])
		switch code {
		case sslMagic, gssMagic:
			// No TLS wired: reply 'N' for no SSL and re-read the real startup
			// message on the same connection (spec 16 §4.2).
			if _, err := c.w.w.Write([]byte{'N'}); err != nil {
				return false, err
			}
			if err := c.w.flush(); err != nil {
				return false, err
			}
			continue
		case cancelMagic:
			// CancelRequest is a one-shot connection; we have no cross-connection
			// cancel registry in this build, so acknowledge by closing (spec 16 §17.1).
			return false, nil
		case protoVersion3:
			if err := c.parseStartupParams(body[4:]); err != nil {
				return false, err
			}
			if err := c.authenticate(); err != nil {
				return false, err
			}
			if err := c.sendStartupStatus(); err != nil {
				return false, err
			}
			return true, c.ready()
		default:
			major := code >> 16
			return false, c.fatalf("08P01", "unsupported protocol version %d.%d", major, code&0xffff)
		}
	}
}

// parseStartupParams reads the null-terminated key/value pairs (spec 16 §17.1).
func (c *Conn) parseStartupParams(p []byte) error {
	br := newBodyReader(p)
	for br.remaining() > 0 {
		key, err := br.cstring()
		if err != nil {
			return err
		}
		if key == "" {
			break // terminating null
		}
		val, err := br.cstring()
		if err != nil {
			return err
		}
		switch key {
		case "user":
			c.user = val
		case "database":
			c.database = val
		}
	}
	if c.user == "" {
		c.user = "vec"
	}
	return nil
}

// authenticate runs the configured auth exchange (spec 16 §4.3).
func (c *Conn) authenticate() error {
	mode := strings.ToLower(c.opts.AuthMode)
	switch mode {
	case "", "none", "trust":
		return c.w.writeAuthenticationOk()
	case "password", "md5", "cleartext":
		// MD5 is accepted as a cleartext fallback: we always request a cleartext
		// password and verify it through the injected Verify func (spec 16 §4.3).
		if err := c.w.writeAuthenticationCleartext(); err != nil {
			return err
		}
		if err := c.w.flush(); err != nil {
			return err
		}
		pw, err := c.readPassword()
		if err != nil {
			return err
		}
		if c.opts.Verify == nil {
			return c.fatalf("28P01", "password authentication is not configured")
		}
		if err := c.opts.Verify(c.user, pw); err != nil {
			return c.fatalf("28P01", "password authentication failed for user %q", c.user)
		}
		return c.w.writeAuthenticationOk()
	default:
		return c.fatalf("28P01", "unsupported auth mode %q", c.opts.AuthMode)
	}
}

// readPassword reads a PasswordMessage ('p') and returns the cleartext password
// (spec 16 §17.2).
func (c *Conn) readPassword() (string, error) {
	if err := c.w.flush(); err != nil {
		return "", err
	}
	f, err := c.r.readFrame()
	if err != nil {
		return "", err
	}
	if f.tag != fePassword {
		return "", errors.New("pgwire: expected PasswordMessage")
	}
	br := newBodyReader(f.body)
	return br.cstring()
}

// sendStartupStatus emits the fixed ParameterStatus set and BackendKeyData
// (spec 16 §4.3, §17.3).
func (c *Conn) sendStartupStatus() error {
	params := [][2]string{
		{"server_version", "15.0 (vec " + c.opts.Version + ")"},
		{"client_encoding", "UTF8"},
		{"DateStyle", "ISO, MDY"},
		{"TimeZone", "UTC"},
		{"integer_datetimes", "on"},
		{"standard_conforming_strings", "on"},
		{"application_name", ""},
		{"is_superuser", "off"},
		{"session_authorization", c.user},
	}
	for _, p := range params {
		if err := c.w.writeParameterStatus(p[0], p[1]); err != nil {
			return err
		}
	}
	pid, key := randomKey()
	return c.w.writeBackendKeyData(pid, key)
}

// randomKey returns a random backend PID and cancel key (spec 16 §4.3).
func randomKey() (int32, int32) {
	var b [8]byte
	_, _ = rand.Read(b[:])
	pid := int32(binary.BigEndian.Uint32(b[:4]) & 0x7fffffff)
	key := int32(binary.BigEndian.Uint32(b[4:]) & 0x7fffffff)
	if pid == 0 {
		pid = 1
	}
	return pid, key
}

// ready flushes a ReadyForQuery in the current transaction state (spec 16 §17.7).
func (c *Conn) ready() error {
	if err := c.w.writeReadyForQuery(byte(c.state)); err != nil {
		return err
	}
	return c.w.flush()
}

// fatalf writes an ErrorResponse, flushes, and returns an error so the caller
// can abort the connection.
func (c *Conn) fatalf(code, format string, args ...any) error {
	e := pgError{code: code, message: sprintf(format, args...)}
	_ = c.w.writeErrorResponse(e)
	_ = c.w.flush()
	return e
}

// loop is the message dispatch loop after startup (spec 16 §4.2, §4.4). It
// handles the simple-query path ('Q') and the extended-query state machine
// (Parse/Bind/Describe/Execute/Sync/Close), plus Terminate.
func (c *Conn) loop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f, err := c.r.readFrame()
		if err != nil {
			return err
		}
		switch f.tag {
		case feQuery:
			if err := c.handleSimpleQuery(ctx, f.body); err != nil {
				return err
			}
		case feParse:
			if err := c.handleParse(f.body); err != nil {
				return err
			}
		case feBind:
			if err := c.handleBind(f.body); err != nil {
				return err
			}
		case feDescribe:
			if err := c.handleDescribe(f.body); err != nil {
				return err
			}
		case feExecute:
			if err := c.handleExecute(ctx, f.body); err != nil {
				return err
			}
		case feSync:
			c.syncReset()
			if err := c.ready(); err != nil {
				return err
			}
		case feFlush:
			if err := c.w.flush(); err != nil {
				return err
			}
		case feClose:
			if err := c.handleClose(f.body); err != nil {
				return err
			}
		case feTerminate:
			return nil
		default:
			// Unknown message: report and continue waiting for Sync.
			if err := c.extendedError(pgError{code: "08P01",
				message: sprintf("unsupported message type %q", f.tag)}); err != nil {
				return err
			}
		}
	}
}

// syncReset clears a failed-transaction state at Sync. A failed transaction
// stays failed until ROLLBACK; outside a block we return to idle (spec 16 §17.7).
func (c *Conn) syncReset() {
	if c.state == txnFailed {
		return
	}
	if c.txn == nil {
		c.state = txnIdle
	}
}
