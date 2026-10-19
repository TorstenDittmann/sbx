package api_test

import (
	"net/http"
	"testing"
)

func TestTemplateWarmAPI(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/templates", h.key, map[string]any{
		"name": "agent", "image": "alpine:3.20", "network": "full",
		"setup": []string{"echo", "ready"}, "warm": 1, "memory_mb": 128,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var tpl struct {
		Name    string   `json:"name"`
		Network string   `json:"network"`
		Warm    int      `json:"warm"`
		Setup   []string `json:"setup"`
	}
	decode(t, resp, &tpl)
	if tpl.Warm != 1 || tpl.Network != "full" || len(tpl.Setup) != 2 {
		t.Fatalf("%+v", tpl)
	}

	resp = h.do(t, http.MethodGet, "/v1/sandboxes?status=warm", h.key, nil)
	var warm struct {
		Sandboxes []struct {
			ID string `json:"id"`
		} `json:"sandboxes"`
	}
	decode(t, resp, &warm)
	if len(warm.Sandboxes) != 1 {
		t.Fatalf("%+v", warm)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"template": "agent", "ttl_seconds": 60})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var sb struct {
		ID      string `json:"id"`
		Network string `json:"network"`
	}
	decode(t, resp, &sb)
	if sb.ID != warm.Sandboxes[0].ID || sb.Network != "full" {
		t.Fatalf("%+v", sb)
	}

	resp = h.do(t, http.MethodGet, "/v1/sandboxes", h.key, nil)
	var listed struct {
		Sandboxes []struct {
			Status string `json:"status"`
		} `json:"sandboxes"`
	}
	decode(t, resp, &listed)
	for _, item := range listed.Sandboxes {
		if item.Status == "warm" {
			t.Fatal("default list included a warm sandbox")
		}
	}

	resp = h.do(t, http.MethodDelete, "/v1/templates/agent", h.key, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
}
