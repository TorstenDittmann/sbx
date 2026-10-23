package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestKeyRegistries(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{
		"name": "ci",
		"registries": []map[string]any{
			{"server": "https://ghcr.io", "username": "bot", "password": "s3cret"},
			{"server": "registry.example.com", "identity_token": "tok"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "tok\"") {
		t.Fatalf("secret returned: %s", raw)
	}
	if !strings.Contains(string(raw), `{"server":"ghcr.io","username":"bot"}`) {
		t.Fatalf("%s", raw)
	}
	id := between(string(raw), `"id":"`, `"`)

	var got struct {
		Registries []map[string]string `json:"registries"`
	}
	decode(t, h.do(t, http.MethodPut, "/v1/keys/"+id+"/registries", h.key, map[string]any{
		"registries": []map[string]any{{"server": "docker.io", "username": "hub", "password": "p"}},
	}), &got)
	if len(got.Registries) != 1 || got.Registries[0]["server"] != "docker.io" || got.Registries[0]["password"] != "" {
		t.Fatalf("%+v", got)
	}
	decode(t, h.do(t, http.MethodGet, "/v1/keys/"+id, h.key, nil), &got)
	if len(got.Registries) != 1 || got.Registries[0]["username"] != "hub" {
		t.Fatalf("%+v", got)
	}
	decode(t, h.do(t, http.MethodPut, "/v1/keys/"+id+"/registries", h.key, map[string]any{"registries": []any{}}), &got)
	if len(got.Registries) != 0 {
		t.Fatalf("%+v", got)
	}

	if resp := h.do(t, http.MethodPut, "/v1/keys/"+id+"/registries", h.key, map[string]any{
		"registries": []map[string]any{{"server": "ghcr.io", "username": "bot"}},
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing password accepted: %s", resp.Status)
	}
	if resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{
		"name": "dup", "registries": []map[string]any{{"server": "ghcr.io", "identity_token": "a"}, {"server": "GHCR.IO", "identity_token": "b"}},
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate accepted: %s", resp.Status)
	}
	if resp := h.do(t, http.MethodPut, "/v1/keys/key_missing/registries", h.key, map[string]any{"registries": []any{}}); resp.StatusCode != http.StatusNotFound {
		t.Fatal(resp.Status)
	}
}

func between(s, start, end string) string {
	_, rest, _ := strings.Cut(s, start)
	v, _, _ := strings.Cut(rest, end)
	return v
}
