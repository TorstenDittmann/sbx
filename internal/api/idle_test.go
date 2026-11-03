package api_test

import (
	"io"
	"net/http"
	"testing"
)

func TestCreateWithIdleAction(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image": "alpine:3.20", "idle_timeout_seconds": 60, "idle_action": "hibernate",
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s", resp.Status, b)
	}
	var sb struct {
		IdleAction string `json:"idle_action"`
	}
	decode(t, resp, &sb)
	if sb.IdleAction != "hibernate" {
		t.Fatalf("idle_action %q", sb.IdleAction)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image": "alpine:3.20", "idle_action": "pause",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("idle_action without a timeout: %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image": "alpine:3.20", "backend": "gvisor", "idle_timeout_seconds": 60, "idle_action": "hibernate",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("hibernate on gvisor: %s", resp.Status)
	}
}
