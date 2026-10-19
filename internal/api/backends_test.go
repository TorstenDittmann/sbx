package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestListBackendsAndTemplateBackend(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodGet, "/v1/backends", h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	var body struct {
		Backends []struct {
			Name      string   `json:"name"`
			Available bool     `json:"available"`
			Networks  []string `json:"networks"`
		} `json:"backends"`
	}
	decode(t, resp, &body)
	if len(body.Backends) != 4 || body.Backends[0].Name != "docker" || !body.Backends[0].Available {
		t.Fatalf("%+v", body.Backends)
	}
	if body.Backends[2].Name != "kata" || body.Backends[2].Available {
		t.Fatalf("%+v", body.Backends)
	}
	resp = h.do(t, http.MethodPost, "/v1/templates", h.key, map[string]any{
		"name": "gv", "image": "alpine:3.20", "backend": "gvisor",
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := json.Marshal(body)
		t.Fatalf("%s %s", resp.Status, b)
	}
	var tpl map[string]any
	decode(t, resp, &tpl)
	if tpl["backend"] != "gvisor" {
		t.Fatalf("%v", tpl)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"template": "gv"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var created struct {
		Backend string `json:"backend"`
		Image   string `json:"image"`
	}
	decode(t, resp, &created)
	if created.Backend != "gvisor" || created.Image != "alpine:3.20" {
		t.Fatalf("%+v", created)
	}
}
