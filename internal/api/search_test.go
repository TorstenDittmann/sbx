package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestListSearchAndLabels(t *testing.T) {
	h := newHarness(t)
	mk := func(template string, labels map[string]string) string {
		t.Helper()
		body := map[string]any{"image": "alpine:3.20", "labels": labels}
		if template != "" {
			body["template"] = template
			delete(body, "image")
		}
		resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatal(resp.Status)
		}
		var created struct {
			ID string `json:"id"`
		}
		decode(t, resp, &created)
		return created.ID
	}
	hansi := mk("base", map[string]string{"project": "Hansi"})
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image": "debian:bookworm-slim", "labels": map[string]string{"project": "other"},
		"env": map[string]string{"SECRET": "not-searched"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var other struct {
		ID string `json:"id"`
	}
	decode(t, resp, &other)

	got := listIDs(t, h, "/v1/sandboxes?q=hansi")
	if len(got) != 1 || got[0] != hansi {
		t.Fatal(got)
	}
	got = listIDs(t, h, "/v1/sandboxes?q=ALPINE")
	if len(got) != 1 || got[0] != hansi {
		t.Fatalf("image search %+v", got)
	}
	got = listIDs(t, h, "/v1/sandboxes?q=debian")
	if len(got) != 1 || got[0] != other.ID {
		t.Fatalf("image search %+v", got)
	}
	got = listIDs(t, h, "/v1/sandboxes?q=not-searched")
	if len(got) != 0 {
		t.Fatalf("env leaked into search %+v", got)
	}
	got = listIDs(t, h, "/v1/sandboxes?q=running")
	if len(got) != 2 {
		t.Fatalf("status search %+v", got)
	}
	got = listIDs(t, h, "/v1/sandboxes?label=project:Hansi")
	if len(got) != 1 || got[0] != hansi {
		t.Fatal(got)
	}
	got = listIDs(t, h, "/v1/sandboxes?template=base&q=project")
	if len(got) != 1 || got[0] != hansi {
		t.Fatal(got)
	}
	got = listIDs(t, h, "/v1/sandboxes?q=missing-token")
	if len(got) != 0 {
		t.Fatal(got)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes?q="+strings.Repeat("z", 128), h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes?q="+strings.Repeat("a", 129), h.key, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes?q=%00", h.key, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
}

func listIDs(t *testing.T, h *harness, path string) []string {
	t.Helper()
	resp := h.do(t, http.MethodGet, path, h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	var body struct {
		Sandboxes []struct {
			ID string `json:"id"`
		} `json:"sandboxes"`
	}
	decode(t, resp, &body)
	out := make([]string, 0, len(body.Sandboxes))
	for _, sb := range body.Sandboxes {
		out = append(out, sb.ID)
	}
	return out
}
