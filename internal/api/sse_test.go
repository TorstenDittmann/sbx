package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withStreamPing(t *testing.T, d time.Duration) {
	t.Helper()
	old := streamPing
	streamPing = d
	t.Cleanup(func() { streamPing = old })
}

func TestSSEPingStartsQuietStream(t *testing.T) {
	withStreamPing(t, 5*time.Millisecond)
	rec := httptest.NewRecorder()
	sse := newSSE(rec)
	time.Sleep(40 * time.Millisecond)
	if !sse.Stop() {
		t.Fatal("a ping should start the stream")
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.HasPrefix(body, ": ping\n\n") {
		t.Fatalf("body %q", body)
	}
	// Nothing writes after Stop, so the handler owns the writer again.
	time.Sleep(20 * time.Millisecond)
	if rec.Body.String() != body {
		t.Fatal("pinged after Stop")
	}
	sse.Send("exit", map[string]int{"exit_code": 0})
	if !strings.HasSuffix(rec.Body.String(), "event: exit\ndata: {\"exit_code\":0}\n\n") {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestSSEStopBeforeStartLeavesWriterUntouched(t *testing.T) {
	withStreamPing(t, time.Hour)
	rec := httptest.NewRecorder()
	sse := newSSE(rec)
	if sse.Stop() || sse.Stop() {
		t.Fatal("stream should not have started")
	}
	writeErr(rec, http.StatusNotFound, "not_found", "missing")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestSSEPingsWatchStream(t *testing.T) {
	withStreamPing(t, 5*time.Millisecond)
	rec := httptest.NewRecorder()
	sse := newSSE(rec)
	sse.Send("ready", map[string]string{"path": "/workspace"})
	time.Sleep(30 * time.Millisecond)
	sse.Stop()
	body := rec.Body.String()
	if !strings.HasPrefix(body, "event: ready\n") || !strings.Contains(body, "\n\n: ping\n\n") {
		t.Fatalf("body %q", body)
	}
}
