package server

import (
	"context"
	"errors"
)

// writeReq is one unit of work for the writer goroutine (spec 16 §16.2). fn does
// the actual mutation; the result travels back on done.
type writeReq struct {
	fn   func() error
	done chan error
}

// errShuttingDown is returned to writers queued when the server stops.
var errShuttingDown = errors.New("server shutting down")

// runWriter is the single-writer pipeline. Every upsert and delete passes through
// this one goroutine, which preserves the single-writer MVCC invariant across all
// client connections (spec 16 §1.3, §9.1). Physical group commit happens in the
// WAL layer; this serialization gives the ordering and the backpressure.
func (s *Server) runWriter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.drainWriter(errShuttingDown)
			return
		case req, ok := <-s.writeCh:
			if !ok {
				return
			}
			req.done <- req.fn()
		}
	}
}

// drainWriter fails every still-queued request with err. It runs once the writer
// loop sees a canceled context so blocked callers are released.
func (s *Server) drainWriter(err error) {
	for {
		select {
		case req, ok := <-s.writeCh:
			if !ok {
				return
			}
			req.done <- err
		default:
			return
		}
	}
}

// write submits fn to the writer pipeline and waits for its result. When the
// queue is full the call blocks, which is the backpressure the spec calls for
// (spec 16 §9.1): write load is pushed back onto the caller rather than buffered
// without bound.
func (s *Server) write(ctx context.Context, fn func() error) error {
	done := make(chan error, 1)
	req := writeReq{fn: fn, done: done}
	select {
	case s.writeCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
