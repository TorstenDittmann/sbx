package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPatchSandboxNetwork(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image":   "alpine:3.20",
		"network": map[string]any{"mode": "allow", "allow": []string{"example.com"}},
	})
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, string(raw))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := decodeRaw(t, raw, &created); err != nil {
		t.Fatal(err)
	}

	resp = h.do(t, http.MethodPatch, "/v1/sandboxes/"+created.ID+"/network", h.key, map[string]any{
		"allow": []string{"203.0.113.0/24"}, "deny": []string{"203.0.113.9"}, "max_connections": 8,
	})
	raw, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status, string(raw))
	}
	var got struct {
		Network  string   `json:"network"`
		Allow    []string `json:"egress_allow"`
		Deny     []string `json:"egress_deny"`
		MaxConns int      `json:"max_connections"`
	}
	if err := decodeRaw(t, raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Network != "allow" || len(got.Allow) != 1 || got.Allow[0] != "203.0.113.0/24" || len(got.Deny) != 1 || got.MaxConns != 8 {
		t.Fatalf("%+v", got)
	}
	spec, _ := h.docker.Spec(created.ID)
	if len(spec.Policy.Allow) != 1 || spec.Policy.Allow[0] != "203.0.113.0/24" {
		t.Fatalf("driver policy = %+v", spec.Policy)
	}

	resp = h.do(t, http.MethodPatch, "/v1/sandboxes/"+created.ID+"/network", h.key, map[string]any{"mode": "none"})
	raw, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "unsupported") {
		t.Fatal(resp.Status, string(raw))
	}
	resp = h.do(t, http.MethodPatch, "/v1/sandboxes/"+created.ID+"/network", h.key, map[string]any{"bogus": true})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodPatch, "/v1/sandboxes/sbx_missing/network", h.key, map[string]any{"deny": []string{}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal(resp.Status)
	}
}
