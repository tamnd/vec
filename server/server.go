package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/tamnd/vec"
	"github.com/tamnd/vec/server/pgwire"
)

// Server holds one open database and the listeners that project it onto the
// network (spec 16 §16.1). One Server owns one *vec.DB; the single-writer
// invariant is kept by routing every write through the writer pipeline.
type Server struct {
	cfg     Config
	db      *vec.DB
	ownDB   bool // close db on Stop only if the server opened it
	auth    *authenticator
	metrics *Metrics
	log     *logger

	writeCh chan writeReq
	ops     *opRegistry

	started time.Time

	mu        sync.Mutex
	listeners []net.Listener
	httpSrvs  []*http.Server
	stopped   bool
	wg        sync.WaitGroup
}

// New opens the database named in the config and builds a server over it. Close
// the server with Stop, which also closes the database it opened.
func New(cfg Config) (*Server, error) {
	opts := []vec.Option{vec.WithBusyTimeout(cfg.BusyTimeout)}
	if cfg.ReadOnly {
		db, err := vec.OpenReadOnly(cfg.Path, opts...)
		if err != nil {
			return nil, err
		}
		return NewWithDB(cfg, db, true)
	}
	db, err := vec.Open(cfg.Path, opts...)
	if err != nil {
		return nil, err
	}
	return NewWithDB(cfg, db, true)
}

// NewWithDB builds a server over an already-open database. When ownDB is true the
// server closes the database on Stop. This is the entry an embedding program uses
// to serve a database it opened itself.
func NewWithDB(cfg Config, db *vec.DB, ownDB bool) (*Server, error) {
	s := &Server{
		cfg:     cfg,
		db:      db,
		ownDB:   ownDB,
		auth:    newAuthenticator(cfg),
		metrics: newMetrics(),
		log:     newLogger(cfg.LogLevel),
		writeCh: make(chan writeReq, max(1, cfg.MaxWriteQueueDepth)),
		ops:     newOpRegistry(),
		started: time.Now(),
	}
	return s, nil
}

// clock returns a monotonic nanosecond reading for latency measurement.
func (s *Server) clock() int64 { return time.Now().UnixNano() }

// Serve starts every configured listener and blocks until ctx is canceled or a
// listener fails, then drains and shuts down (spec 16 §22). It is the entry the
// serve subcommand calls.
func (s *Server) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The writer pipeline runs for the life of the server.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runWriter(ctx)
	}()

	tlsCfg, err := s.tlsConfig()
	if err != nil {
		return err
	}

	errCh := make(chan error, 4)
	if err := s.startREST(ctx, tlsCfg, errCh); err != nil {
		return err
	}
	if err := s.startGRPC(ctx, tlsCfg, errCh); err != nil {
		return err
	}
	if err := s.startPG(ctx, tlsCfg, errCh); err != nil {
		return err
	}
	if err := s.startMetrics(errCh); err != nil {
		return err
	}

	s.log.infof("vec serve ready: grpc=%s rest=%s pg=%s version=%s",
		s.cfg.GRPCAddr, s.cfg.RESTAddr, s.cfg.PGAddr, vec.Version())

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
		if serveErr != nil {
			s.log.errorf("listener failed: %v", serveErr)
		}
	}
	s.Stop()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

// startREST starts the REST/JSON listener if configured.
func (s *Server) startREST(ctx context.Context, tlsCfg *tls.Config, errCh chan<- error) error {
	if s.cfg.RESTAddr == "" {
		return nil
	}
	ln, err := s.listen(s.cfg.RESTAddr, tlsCfg)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.restHandler(), ReadHeaderTimeout: 10 * time.Second}
	s.track(srv)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		errCh <- srv.Serve(ln)
	}()
	return nil
}

// startGRPC starts the gRPC listener if configured.
func (s *Server) startGRPC(ctx context.Context, tlsCfg *tls.Config, errCh chan<- error) error {
	if s.cfg.GRPCAddr == "" {
		return nil
	}
	ln, err := s.listen(s.cfg.GRPCAddr, tlsCfg)
	if err != nil {
		return err
	}
	srv := s.grpcServer()
	s.track(srv)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		errCh <- srv.Serve(ln)
	}()
	return nil
}

// startPG starts the PostgreSQL wire listener if configured.
func (s *Server) startPG(ctx context.Context, tlsCfg *tls.Config, errCh chan<- error) error {
	if s.cfg.PGAddr == "" {
		return nil
	}
	ln, err := s.listen(s.cfg.PGAddr, tlsCfg)
	if err != nil {
		return err
	}
	s.trackListener(ln)
	opts := pgwire.Options{
		DB:       s.db,
		Version:  vec.Version(),
		AuthMode: s.pgAuthMode(),
		Verify:   s.verifyPG,
		Logger:   func(format string, args ...any) { s.log.debugf(format, args...) },
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		errCh <- pgwire.Serve(ctx, ln, opts)
	}()
	return nil
}

// startMetrics starts the separate Prometheus endpoint if configured.
func (s *Server) startMetrics(errCh chan<- error) error {
	if s.cfg.MetricsAddr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", s.cfg.MetricsAddr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.metrics)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s.track(srv)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		errCh <- srv.Serve(ln)
	}()
	return nil
}

// pgAuthMode maps the server auth mode to a PG wire auth mode. Token auth maps to
// cleartext password where the token is the password; none maps to trust.
func (s *Server) pgAuthMode() string {
	if s.cfg.AuthMode == "none" {
		return "trust"
	}
	return "password"
}

// verifyPG checks a PG wire password against the token table.
func (s *Server) verifyPG(user, password string) error {
	if s.cfg.AuthMode == "none" {
		return nil
	}
	_, err := s.auth.verify(password)
	return err
}

// listen opens a TCP listener, wrapping it in TLS when a certificate is set.
func (s *Server) listen(addr string, tlsCfg *tls.Config) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	s.trackListener(ln)
	return ln, nil
}

// tlsConfig builds the server TLS config, or nil when TLS is disabled.
func (s *Server) tlsConfig() (*tls.Config, error) {
	if !s.cfg.TLSEnabled() {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("load tls keypair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

func (s *Server) track(srv *http.Server) {
	s.mu.Lock()
	s.httpSrvs = append(s.httpSrvs, srv)
	s.mu.Unlock()
}

func (s *Server) trackListener(ln net.Listener) {
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.mu.Unlock()
}

// Stop drains the listeners, stops the writer pipeline, and closes the database
// the server owns (spec 16 §22.2). It is safe to call more than once.
func (s *Server) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	srvs := s.httpSrvs
	lns := s.listeners
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownGrace)
	defer cancel()
	for _, srv := range srvs {
		_ = srv.Shutdown(ctx)
	}
	for _, ln := range lns {
		_ = ln.Close()
	}
	// Closing the write channel stops the writer goroutine after it drains.
	close(s.writeCh)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if s.ownDB {
		_ = s.db.Close()
	}
}

// SignalContext returns a context canceled on SIGINT or SIGTERM, the standard
// graceful-shutdown trigger for the serve subcommand (spec 16 §10.5).
func SignalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	notifySignals(ch)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
