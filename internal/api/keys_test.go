package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestKeyRevokeAndExpiry(t *testing.T) {
	h := newHarness(t)
	var keys struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	decode(t, h.do(t, http.MethodGet, "/v1/keys", h.key, nil), &keys)
	if len(keys.Keys) != 1 {
		t.Fatalf("%+v", keys)
	}
	adminID := keys.Keys[0].ID
	if resp := h.do(t, http.MethodDelete, "/v1/keys/"+adminID, h.key, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("revoking the last admin key = %s", resp.Status)
	}

	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "ci"})
	decode(t, resp, &created)
	if resp := h.do(t, http.MethodGet, "/v1/sandboxes", created.Key, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	if resp := h.do(t, http.MethodDelete, "/v1/keys/"+created.ID, h.key, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
	if resp := h.do(t, http.MethodGet, "/v1/sandboxes", created.Key, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key still works: %s", resp.Status)
	}
	var got struct {
		RevokedAt *time.Time `json:"revoked_at"`
	}
	decode(t, h.do(t, http.MethodGet, "/v1/keys/"+created.ID, h.key, nil), &got)
	if got.RevokedAt == nil {
		t.Fatal("revoked_at missing")
	}
	if resp := h.do(t, http.MethodDelete, "/v1/keys/key_missing", h.key, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatal(resp.Status)
	}

	if resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "old", "expires_at": time.Now().Add(-time.Minute)}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("past expiry accepted: %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "short", "expires_at": time.Now().Add(time.Second)})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	decode(t, resp, &created)
	if resp := h.do(t, http.MethodGet, "/v1/sandboxes", created.Key, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	time.Sleep(1100 * time.Millisecond)
	if resp := h.do(t, http.MethodGet, "/v1/sandboxes", created.Key, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired key still works: %s", resp.Status)
	}
}

func TestKeyMaxModeValidation(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		network map[string]any
		want    int
	}{
		{map[string]any{"max_mode": "open"}, http.StatusBadRequest},
		{map[string]any{"mode": "full", "max_mode": "none"}, http.StatusBadRequest},
		{map[string]any{"max_mode": "allow"}, http.StatusBadRequest},
		{map[string]any{"max_mode": "allow", "allow": []string{"example.com"}}, http.StatusCreated},
		{map[string]any{"mode": "none", "max_mode": "full"}, http.StatusCreated},
	} {
		resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "n", "network": tc.network})
		if resp.StatusCode != tc.want {
			t.Fatalf("%v = %s, want %d", tc.network, resp.Status, tc.want)
		}
	}
	var created struct {
		Key string `json:"key"`
	}
	decode(t, h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "offline", "network": map[string]any{"max_mode": "none"}}), &created)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", created.Key, map[string]any{"image": "alpine:3.20", "network": map[string]any{"mode": "full"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("max_mode none created a full sandbox: %s", resp.Status)
	}
}
