package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/core"
)

// streamPing is how often a quiet exec, process, or watch stream sends a
// comment line. Like the lifecycle event stream, it keeps proxies and load
// balancers from closing a connection while a command runs without output.
var streamPing = 15 * time.Second

// sseStream writes server-sent events. Headers go out with the first event,
// so an error before any output can still be a plain JSON error response.
// Once started, a background ticker sends ": ping" comments; the first tick
// starts the stream itself, so a silent command does not leave the client
// waiting for headers.
type sseStream struct {
	w       http.ResponseWriter
	flusher http.Flusher

	mu      sync.Mutex
	started bool
	stopped bool
	done    chan struct{}
}

func newSSE(w http.ResponseWriter) *sseStream {
	flusher, _ := w.(http.Flusher)
	s := &sseStream{w: w, flusher: flusher, done: make(chan struct{})}
	go s.pinger(streamPing)
	return s
}

func (s *sseStream) pinger(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			if !s.stopped {
				s.startLocked()
				_, _ = fmt.Fprint(s.w, ": ping\n\n")
				s.flushLocked()
			}
			s.mu.Unlock()
		}
	}
}

func (s *sseStream) startLocked() {
	if s.started {
		return
	}
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.started = true
}

func (s *sseStream) flushLocked() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// Start sends the headers now.
func (s *sseStream) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked()
	s.flushLocked()
}

// Send writes one event with a JSON payload.
func (s *sseStream) Send(event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked()
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	s.flushLocked()
}

// SendError writes an error event built from a manager error.
func (s *sseStream) SendError(err error) {
	var op *core.OpError
	code, msg := "driver", err.Error()
	if errors.As(err, &op) {
		code, msg = op.Code, op.Msg
	}
	s.Send("error", map[string]string{"code": code, "message": msg})
}

// Stop ends the pings and reports whether the headers went out. After it
// returns nothing else writes on its own, so a handler whose stream never
// started can still answer with a plain JSON error. Handlers must call it
// before returning, since the ResponseWriter is not usable after that; it is
// safe to call more than once.
func (s *sseStream) Stop() (started bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		close(s.done)
	}
	return s.started
}
