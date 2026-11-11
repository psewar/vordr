package hub

import (
	"context"

	"github.com/psewar/vordr/internal/wire"
)

// open counts a connection the server serves from now on, so that Close ends it and waits for its handler. Once Close
// has begun it counts nothing and says so; the caller then ends the connection without a session.
func (s *Server) open(c *wire.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.conns[c] = struct{}{}
	s.work.Add(1)
	return true
}

// release: the connection's handler is done, including everything it wrote.
func (s *Server) release(c *wire.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	s.work.Done()
}

// spawn runs f in a goroutine that Close waits for. Once Close has begun it starts nothing and reports false. The
// caller must not hold s.mu.
func (s *Server) spawn(f func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.work.Add(1)
	go func() {
		defer s.work.Done()
		f()
	}()
	return true
}

// stopping: Close has begun. A wait that ends then records nothing; the run stays as it is, and the agent's next hello
// settles it (ADR-0114), as after any restart of the hub.
func (s *Server) stopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// untilClose is ctx, ended as well when Close begins: for the waits that outlast a session (a job's result, a backup
// before an install, the agent that starts next), which Close would otherwise sit out.
func (s *Server) untilClose(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.closed, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// Close ends every agent and Home Assistant connection and waits until the work they started is done: the messages
// being handled, the jobs being waited for, the next job of a queue (ADR-0142). From then on the server serves no new
// connection and starts no new work, and the waits for jobs end without a record. It reports whether all of it ended
// before ctx did. The loops the caller started itself (backup check, rollout, automatic installs, packages) end with
// their own context.
func (s *Server) Close(ctx context.Context) bool {
	s.mu.Lock()
	s.closing = true
	conns := make([]*wire.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	s.endClosed()
	for _, c := range conns {
		_ = c.Close() // its handler's read ends; the handler drops the session and returns
	}
	done := make(chan struct{})
	go func() {
		s.work.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
