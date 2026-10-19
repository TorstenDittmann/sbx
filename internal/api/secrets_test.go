package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSecretsAreNotReturned(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image":   "alpine:3.20",
		"env":     map[string]string{"FOO": "bar"},
		"secrets": map[string]string{"TOKEN": "sekret"},
	})
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, string(raw))
	}
	if strings.Contains(string(raw), "sekret") || !strings.Contains(string(raw), "TOKEN") || !strings.Contains(string(raw), "bar") {
		t.Fatal(string(raw))
	}
	var created struct {
		ID         string   `json:"id"`
		SecretKeys []string `json:"secret_keys"`
	}
	if err := decodeRaw(t, raw, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.SecretKeys) != 1 || created.SecretKeys[0] != "TOKEN" {
		t.Fatal(created.SecretKeys)
	}

	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID, h.key, nil)
	raw, _ = io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "sekret") {
		t.Fatal(string(raw))
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes", h.key, nil)
	raw, _ = io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "sekret") {
		t.Fatal(string(raw))
	}

	resp = h.do(t, http.MethodPut, "/v1/sandboxes/"+created.ID+"/secrets", h.key, map[string]any{
		"secrets": map[string]string{"OTHER": "next-secret"},
	})
	raw, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), "next-secret") || !strings.Contains(string(raw), "OTHER") {
		t.Fatal(resp.Status, string(raw))
	}
}

func decodeRaw(t *testing.T, raw []byte, dst any) error {
	t.Helper()
	return json.Unmarshal(raw, dst)
}
