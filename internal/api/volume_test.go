package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestVolumeAPI(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/volumes", h.key, map[string]string{"name": "project"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, readBody(t, resp))
	}
	var created struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Backend string `json:"backend"`
	}
	decode(t, resp, &created)
	if created.Name != "project" || created.Backend != "docker" || created.ID == "" {
		t.Fatalf("%+v", created)
	}

	resp = h.do(t, http.MethodGet, "/v1/volumes", h.key, nil)
	var list struct {
		Volumes []struct {
			ID string `json:"id"`
		} `json:"volumes"`
	}
	decode(t, resp, &list)
	if resp.StatusCode != http.StatusOK || len(list.Volumes) != 1 {
		t.Fatalf("%d %+v", resp.StatusCode, list)
	}

	resp = h.do(t, http.MethodGet, "/v1/volumes/"+created.ID, h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image": "alpine:3.20",
		"mounts": []map[string]string{
			{"volume_id": created.ID, "path": "/workspace"},
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, readBody(t, resp))
	}
	var sb struct {
		ID     string `json:"id"`
		Mounts []struct {
			VolumeID string `json:"volume_id"`
			Path     string `json:"path"`
			Source   string `json:"source"`
		} `json:"mounts"`
	}
	decode(t, resp, &sb)
	if len(sb.Mounts) != 1 || sb.Mounts[0].VolumeID != created.ID || sb.Mounts[0].Path != "/workspace" || sb.Mounts[0].Source != "" {
		t.Fatalf("%+v", sb.Mounts)
	}

	resp = h.do(t, http.MethodDelete, "/v1/volumes/"+created.ID, h.key, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatal(resp.Status, readBody(t, resp))
	}

	resp = h.do(t, http.MethodDelete, "/v1/sandboxes/"+sb.ID, h.key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodDelete, "/v1/volumes/"+created.ID, h.key, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status, readBody(t, resp))
	}
	if h.docker.HasVolume("sbx-" + created.ID[4:]) {
		t.Fatal("volume still present")
	}

	resp = h.do(t, http.MethodPost, "/v1/volumes", h.key, map[string]string{"name": "project"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status, readBody(t, resp))
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image":  "alpine:3.20",
		"mounts": []map[string]string{{"volume_id": "vol_doesnotexist000", "path": "/workspace/src"}},
	})
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatal(resp.Status, readBody(t, resp))
	}

	keyResp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "other", "scopes": []string{"sandbox"}})
	var keyBody struct {
		Key string `json:"key"`
	}
	decode(t, keyResp, &keyBody)
	resp = h.do(t, http.MethodGet, "/v1/volumes", keyBody.Key, nil)
	var other struct {
		Volumes []any `json:"volumes"`
	}
	decode(t, resp, &other)
	if len(other.Volumes) != 0 {
		t.Fatalf("sandbox key saw %d volumes", len(other.Volumes))
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestVolumeJSONHasNoDockerName(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/volumes", h.key, map[string]string{"name": "cache", "backend": "docker"})
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["docker_name"]; ok {
		t.Fatal(string(raw))
	}
}
