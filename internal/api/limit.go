package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TorstenDittmann/sbx/internal/core"
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

// bodyLimit is the largest body a request may send, and the settings that
// raise it, so a 413 tells the caller what to change.
type bodyLimit struct {
	max  int64
	keys string
}

func (l bodyLimit) message() string {
	return fmt.Sprintf("request body is larger than the %s limit (%s)", core.FormatBytes(int(l.max)), l.keys)
}

func (s *Server) uploadLimit() bodyLimit {
	return bodyLimit{int64(s.mgr.Config().Limits.MaxUploadBytes), "limits.max_upload_bytes, SBX_LIMITS_MAX_UPLOAD_BYTES"}
}

func (s *Server) archiveLimit() bodyLimit {
	return bodyLimit{int64(s.mgr.Config().Limits.MaxArchiveBytes), "limits.max_archive_bytes, SBX_LIMITS_MAX_ARCHIVE_BYTES"}
}

func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := s.uploadLimit()
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files/archive") {
			l = s.archiveLimit()
		}
		if l.max > 0 && r.ContentLength > l.max {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", l.message())
			return
		}
		next.ServeHTTP(w, r)
	})
}
