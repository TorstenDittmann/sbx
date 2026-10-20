package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestBuildsAndTemplateStepsAPI(t *testing.T) {
	h := newHarness(t)
	steps := []map[string]any{
		{"packages": []string{"ffmpeg", "git"}},
		{"env": map[string]string{"NODE_ENV": "production"}},
		{"workdir": "/app"},
		{"run": "npm install"},
	}
	resp := h.do(t, http.MethodPost, "/v1/builds", h.key, map[string]any{"image": "node:22", "steps": steps})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var built struct {
		ID     string           `json:"id"`
		Image  string           `json:"image"`
		Base   string           `json:"base"`
		Steps  []map[string]any `json:"steps"`
		Cached bool             `json:"cached"`
	}
	decode(t, resp, &built)
	if built.ID == "" || built.Image != "sbx-template:"+built.ID || built.Base != "node:22" || len(built.Steps) != 4 || built.Cached {
		t.Fatalf("%+v", built)
	}
	resp = h.do(t, http.MethodPost, "/v1/builds", h.key, map[string]any{"image": "node:22", "steps": steps})
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "node:22", "steps": steps, "ttl_seconds": 60})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var sb struct {
		Image string `json:"image"`
	}
	decode(t, resp, &sb)
	if sb.Image != built.Image {
		t.Fatalf("image %q, want %q", sb.Image, built.Image)
	}

	resp = h.do(t, http.MethodPost, "/v1/templates", h.key, map[string]any{"name": "media", "image": "node:22", "steps": steps})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var tpl struct {
		Steps []map[string]any `json:"steps"`
	}
	decode(t, resp, &tpl)
	if len(tpl.Steps) != 4 || tpl.Steps[3]["run"] != "npm install" {
		t.Fatalf("%+v", tpl)
	}
	if got := len(h.docker.Builds()); got != 1 {
		t.Fatalf("builds = %d", got)
	}

	resp = h.do(t, http.MethodPost, "/v1/builds", h.key, map[string]any{"image": "node:22", "steps": []map[string]any{{"run": "a", "workdir": "/b"}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}

	resp = h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "ci", "scopes": []string{"sandbox"}})
	var created struct {
		Key string `json:"key"`
	}
	decode(t, resp, &created)
	resp = h.do(t, http.MethodPost, "/v1/builds", created.Key, map[string]any{"image": "node:22", "steps": steps})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	resp.Body.Close()
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", created.Key, map[string]any{"template": "media", "ttl_seconds": 60})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	decode(t, resp, &sb)
	if !strings.HasPrefix(sb.Image, "sbx-template:") {
		t.Fatal(sb.Image)
	}
}
