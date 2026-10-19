package api

import (
	"net/http"
	"sync"
	"time"
)

type window struct {
	start time.Time
	n     int
}

type limiter struct {
	mu     sync.Mutex
	limit  int
	hits   map[string]*window
	period time.Duration
}

func newLimiter(perMinute int) *limiter {
	return &limiter{limit: perMinute, hits: map[string]*window{}, period: time.Minute}
}

func (l *limiter) allow(key string) bool {
	if l == nil || l.limit <= 0 || key == "" {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.hits[key]
	if w == nil || now.Sub(w.start) >= l.period {
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	if w.n >= l.limit {
		return false
	}
	w.n++
	return true
}

func (s *Server) limitBody(next http.Handler) http.Handler {
	max := int64(s.mgr.Config().Limits.MaxUploadBytes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if max > 0 && r.ContentLength > max {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
			return
		}
		next.ServeHTTP(w, r)
	})
}
