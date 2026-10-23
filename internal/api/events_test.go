package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type lifecycleSSE struct {
	id    string
	event string
	data  string
}

// readEvent returns the next SSE event, skipping comments.
func readEvent(t *testing.T, r *bufio.Reader) lifecycleSSE {
	t.Helper()
	var ev lifecycleSSE
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if ev.event != "" {
				return ev
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id: "):
			ev.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			ev.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func (h *harness) openEvents(t *testing.T, ctx context.Context, query, lastID string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/v1/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("events %s %s", resp.Status, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	return resp, bufio.NewReader(resp.Body)
}

func TestEventStream(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The stream opens before anything happened, with no cursor.
	resp, r := h.openEvents(t, ctx, "", "")
	defer resp.Body.Close()

	created := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	var sb struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&sb); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	ev := readEvent(t, r)
	var body struct {
		ID        int64          `json:"id"`
		Type      string         `json:"type"`
		SandboxID string         `json:"sandbox_id"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(ev.data), &body); err != nil {
		t.Fatal(err)
	}
	if ev.event != "sandbox.created" || body.Type != ev.event || body.SandboxID != sb.ID || body.Data["status"] != "running" {
		t.Fatalf("created event %+v %+v", ev, body)
	}
	firstID := ev.id

	del := h.do(t, http.MethodDelete, "/v1/sandboxes/"+sb.ID, h.key, nil)
	del.Body.Close()
	if ev := readEvent(t, r); ev.event != "sandbox.deleted" {
		t.Fatalf("deleted event %+v", ev)
	}
	resp.Body.Close()

	// Reconnecting with Last-Event-ID replays what came after it.
	resp2, r2 := h.openEvents(t, ctx, "?sandbox="+sb.ID, firstID)
	defer resp2.Body.Close()
	if ev := readEvent(t, r2); ev.event != "sandbox.deleted" {
		t.Fatalf("replay %+v", ev)
	}
}

func TestEventStreamBadCursor(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodGet, "/v1/events?after=nope", h.key, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
