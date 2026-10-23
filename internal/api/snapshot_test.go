package api_test

import (
	"io"
	"net/http"
	"testing"
)

func TestSnapshotAPI(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"template": "base", "ttl_seconds": 60, "env": map[string]string{"MODE": "dev"},
		"secrets": map[string]string{"TOKEN": "s3cret"},
	})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	resp = h.doRaw(t, http.MethodPut, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/a.txt", h.key, "application/octet-stream", []byte("prepared"))
	resp.Body.Close()

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/snapshot", h.key, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var plain struct {
		ID        string  `json:"id"`
		Image     string  `json:"image"`
		SandboxID string  `json:"sandbox_id"`
		Base      string  `json:"base"`
		Workspace bool    `json:"workspace"`
		Template  *string `json:"template"`
	}
	decode(t, resp, &plain)
	if plain.ID == "" || plain.Image != "sbx-template:"+plain.ID || plain.SandboxID != created.ID || plain.Base != "alpine:3.20" || !plain.Workspace || plain.Template != nil {
		t.Fatalf("%+v", plain)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/snapshot", h.key, map[string]any{"template": "prepared", "description": "forked"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var named struct {
		Image    string `json:"image"`
		Template struct {
			Name        string            `json:"name"`
			Image       string            `json:"image"`
			Description string            `json:"description"`
			Env         map[string]string `json:"env"`
		} `json:"template"`
	}
	decode(t, resp, &named)
	tpl := named.Template
	if tpl.Name != "prepared" || tpl.Image != named.Image || tpl.Description != "forked" || tpl.Env["MODE"] != "dev" || tpl.Env["TOKEN"] != "" {
		t.Fatalf("%+v", named)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/snapshot", h.key, map[string]any{"template": "prepared"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status)
	}
	resp.Body.Close()

	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"template": "prepared", "ttl_seconds": 60})
	var fork struct {
		ID    string            `json:"id"`
		Image string            `json:"image"`
		Env   map[string]string `json:"env"`
	}
	decode(t, resp, &fork)
	if fork.Image != named.Image || fork.Env["MODE"] != "dev" {
		t.Fatalf("%+v", fork)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+fork.ID+"/files/content?path=/workspace/a.txt", h.key, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "prepared" {
		t.Fatalf("%s %q", resp.Status, body)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/pause", h.key, nil)
	resp.Body.Close()
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/snapshot", h.key, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status)
	}
	resp.Body.Close()

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+fork.ID+"/snapshot", h.key, map[string]any{"bogus": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	resp.Body.Close()
}

func TestSnapshotNeedsBuildRightsAndBackend(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "ci", "scopes": []string{"sandbox"}})
	var key struct {
		Key string `json:"key"`
	}
	decode(t, resp, &key)
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", key.Key, map[string]any{"template": "base", "ttl_seconds": 60})
	var own struct {
		ID string `json:"id"`
	}
	decode(t, resp, &own)
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+own.ID+"/snapshot", key.Key, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	resp.Body.Close()

	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"template": "base", "backend": "gvisor", "ttl_seconds": 60})
	var gv struct {
		ID string `json:"id"`
	}
	decode(t, resp, &gv)
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+gv.ID+"/snapshot", h.key, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, resp, &e)
	if e.Error.Code != "unsupported" {
		t.Fatalf("%+v", e)
	}
	if got := len(h.docker.Snapshots()); got != 0 {
		t.Fatalf("snapshots = %d", got)
	}
}
