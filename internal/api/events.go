package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// eventPing keeps proxies from closing a quiet event stream.
const eventPing = 15 * time.Second

type eventResponse struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	SandboxID string         `json:"sandbox_id"`
	Time      time.Time      `json:"time"`
	Data      map[string]any `json:"data"`
}

// streamEvents is GET /v1/events: sandbox lifecycle events as server-sent
// events. Each event's SSE id is its sequence number, so a reconnect with
// Last-Event-ID (or ?after=) resumes without gaps. Without either, the
// stream starts with the next event.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	after := int64(-1)
	raw := r.Header.Get("Last-Event-ID")
	if q := r.URL.Query().Get("after"); q != "" {
		raw = q
	}
	if raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid", "after must be a non-negative event id")
			return
		}
		after = n
	}
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	headerSent := false
	err := s.mgr.Events(r.Context(), actor(r), core.EventsInput{
		After:     after,
		SandboxID: r.URL.Query().Get("sandbox"),
		Ready: func(cursor int64) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			headerSent = true
			// A bare id sets the client's Last-Event-ID without an event, so
			// a reconnect resumes here even if nothing happened in between.
			fmt.Fprintf(w, "id: %d\n\n", cursor)
			flush()
		},
		Ping: func() error {
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return err
			}
			flush()
			return nil
		},
		PingEvery: eventPing,
	}, func(ev store.Event) error {
		b, err := json.Marshal(eventJSON(ev))
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, b); err != nil {
			return err
		}
		flush()
		return nil
	})
	if err != nil && !headerSent {
		writeManager(w, err)
		return
	}
	if err != nil && !errors.Is(err, r.Context().Err()) {
		s.log.Debug("event stream ended", "err", err)
	}
}

func eventJSON(ev store.Event) eventResponse {
	return eventResponse{ID: ev.Seq, Type: ev.Type, SandboxID: ev.SandboxID, Time: ev.Time, Data: ev.Data}
}
