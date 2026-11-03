package api_test

import (
	"io"
	"net/http"
	"testing"
)

type createdKey struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

func (h *harness) newKey(t *testing.T, body map[string]any) createdKey {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/v1/keys", h.key, body)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create key %v: %s %s", body, resp.Status, b)
	}
	var k createdKey
	decode(t, resp, &k)
	return k
}

func (h *harness) newSandbox(t *testing.T, token string, body map[string]any) (string, int) {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", token, body)
	if resp.StatusCode != http.StatusCreated {
		resp.Body.Close()
		return "", resp.StatusCode
	}
	var sb struct {
		ID string `json:"id"`
	}
	decode(t, resp, &sb)
	return sb.ID, resp.StatusCode
}

func TestReadOnlyKey(t *testing.T) {
	h := newHarness(t)
	writer := h.newKey(t, map[string]any{"name": "w", "project": "acme"})
	reader := h.newKey(t, map[string]any{"name": "r", "scopes": []string{"read"}, "project": "acme"})

	sbID, code := h.newSandbox(t, writer.Key, map[string]any{"image": "alpine:3.20"})
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}

	for _, path := range []string{"/v1/sandboxes", "/v1/sandboxes/" + sbID, "/v1/templates", "/v1/backends", "/v1/volumes", "/v1/me"} {
		if resp := h.do(t, http.MethodGet, path, reader.Key, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with read key = %s", path, resp.Status)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/sandboxes"},
		{http.MethodDelete, "/v1/sandboxes/" + sbID},
		{http.MethodPost, "/v1/sandboxes/" + sbID + "/exec"},
		{http.MethodPost, "/v1/sandboxes/" + sbID + "/pause"},
		{http.MethodPut, "/v1/sandboxes/" + sbID + "/files/content?path=/tmp/x"},
		{http.MethodPost, "/v1/volumes"},
		{http.MethodPost, "/v1/keys"},
	} {
		if resp := h.do(t, tc.method, tc.path, reader.Key, map[string]any{}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with read key = %s, want 403", tc.method, tc.path, resp.Status)
		}
	}
	// /proc/1/environ holds the sandbox's secrets, which the API never
	// returns, so a read key may not download the kernel's files.
	for _, p := range []string{"/proc/1/environ", "/workspace/../proc/self/environ", "/sys/kernel", "/dev"} {
		for _, route := range []string{"/files/content", "/files/archive"} {
			resp := h.do(t, http.MethodGet, "/v1/sandboxes/"+sbID+route+"?path="+p, reader.Key, nil)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("GET %s %s with read key = %s, want 403", route, p, resp.Status)
			}
		}
	}
	if resp := h.do(t, http.MethodGet, "/v1/sandboxes/"+sbID+"/files/content?path=/proc/1/environ", writer.Key, nil); resp.StatusCode == http.StatusForbidden {
		t.Errorf("GET /proc/1/environ with sandbox key = %s, want it allowed", resp.Status)
	}
}

func TestProjectKeysShareVisibility(t *testing.T) {
	h := newHarness(t)
	a := h.newKey(t, map[string]any{"name": "a", "project": "acme"})
	b := h.newKey(t, map[string]any{"name": "b", "project": "acme"})
	other := h.newKey(t, map[string]any{"name": "o", "project": "globex"})
	loner := h.newKey(t, map[string]any{"name": "l"})

	sbID, code := h.newSandbox(t, a.Key, map[string]any{"image": "alpine:3.20"})
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if resp := h.do(t, http.MethodGet, "/v1/sandboxes/"+sbID, b.Key, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("same project = %s", resp.Status)
	}
	for _, k := range []createdKey{other, loner} {
		if resp := h.do(t, http.MethodGet, "/v1/sandboxes/"+sbID, k.Key, nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("outside project = %s", resp.Status)
		}
	}
	count := func(token string) int {
		var list struct {
			Sandboxes []struct {
				ID string `json:"id"`
			} `json:"sandboxes"`
		}
		decode(t, h.do(t, http.MethodGet, "/v1/sandboxes", token, nil), &list)
		return len(list.Sandboxes)
	}
	if n := count(b.Key); n != 1 {
		t.Fatalf("project list = %d", n)
	}
	if n := count(other.Key); n != 0 {
		t.Fatalf("other project list = %d", n)
	}
	// A project member may act on the sandbox, not only see it.
	if resp := h.do(t, http.MethodDelete, "/v1/sandboxes/"+sbID, b.Key, nil); resp.StatusCode/100 != 2 {
		t.Fatalf("project delete = %s", resp.Status)
	}

	// Volumes follow the same rule.
	resp := h.do(t, http.MethodPost, "/v1/volumes", a.Key, map[string]any{"name": "data"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("volume = %s", resp.Status)
	}
	var vol struct {
		ID string `json:"id"`
	}
	decode(t, resp, &vol)
	if resp := h.do(t, http.MethodGet, "/v1/volumes/"+vol.ID, b.Key, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("project volume = %s", resp.Status)
	}
	if resp := h.do(t, http.MethodGet, "/v1/volumes/"+vol.ID, other.Key, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other project volume = %s", resp.Status)
	}
}

func TestKeyProjectValidation(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"name": "n", "project": "-bad"}, http.StatusBadRequest},
		{map[string]any{"name": "n", "project": "has space"}, http.StatusBadRequest},
		{map[string]any{"name": "n", "project": "acme", "scopes": []string{"admin"}}, http.StatusBadRequest},
		{map[string]any{"name": "n", "scopes": []string{"owner"}}, http.StatusBadRequest},
		{map[string]any{"name": "n", "quota": map[string]any{"max_cpu": -1}}, http.StatusBadRequest},
		{map[string]any{"name": "n", "project": "team-a.prod_1", "scopes": []string{"read"}}, http.StatusCreated},
	} {
		resp := h.do(t, http.MethodPost, "/v1/keys", h.key, tc.body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%v = %s, want %d", tc.body, resp.Status, tc.want)
		}
	}
}

func TestKeyQuotas(t *testing.T) {
	h := newHarness(t) // server default: 2 sandboxes per key
	k := h.newKey(t, map[string]any{"name": "q", "quota": map[string]any{"max_sandboxes": 3, "max_cpu": 1.5, "max_memory_mb": 1024}})

	small := map[string]any{"image": "alpine:3.20", "cpu": 0.5, "memory_mb": 256}
	for i := 0; i < 3; i++ {
		if _, code := h.newSandbox(t, k.Key, small); code != http.StatusCreated {
			t.Fatalf("sandbox %d = %d", i, code)
		}
	}
	// The key quota of 3 replaces the server default of 2, and it is now full.
	if _, code := h.newSandbox(t, k.Key, small); code != http.StatusTooManyRequests {
		t.Fatalf("over count = %d", code)
	}

	var me struct {
		Limits struct {
			MaxSandboxes int `json:"max_sandboxes"`
		} `json:"limits"`
		Usage struct {
			Sandboxes int     `json:"sandboxes"`
			CPU       float64 `json:"cpu"`
			MemoryMB  int     `json:"memory_mb"`
		} `json:"usage"`
	}
	decode(t, h.do(t, http.MethodGet, "/v1/me", k.Key, nil), &me)
	if me.Limits.MaxSandboxes != 3 || me.Usage.Sandboxes != 3 || me.Usage.CPU != 1.5 || me.Usage.MemoryMB != 768 {
		t.Fatalf("me = %+v", me)
	}

	// Raise the count; CPU is now the binding limit.
	resp := h.do(t, http.MethodPut, "/v1/keys/"+k.ID+"/quota", h.key, map[string]any{"max_sandboxes": 10, "max_cpu": 1.5, "max_memory_mb": 1024})
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	resp.Body.Close()
	if _, code := h.newSandbox(t, k.Key, small); code != http.StatusTooManyRequests {
		t.Fatalf("over cpu = %d", code)
	}
	// Memory binds next.
	h.do(t, http.MethodPut, "/v1/keys/"+k.ID+"/quota", h.key, map[string]any{"max_sandboxes": 10, "max_cpu": 4, "max_memory_mb": 1024}).Body.Close()
	if _, code := h.newSandbox(t, k.Key, map[string]any{"image": "alpine:3.20", "cpu": 0.5, "memory_mb": 512}); code != http.StatusTooManyRequests {
		t.Fatalf("over memory = %d", code)
	}
	if _, code := h.newSandbox(t, k.Key, small); code != http.StatusCreated {
		t.Fatalf("exactly at memory quota = %d", code)
	}

	if resp := h.do(t, http.MethodPut, "/v1/keys/key_missing/quota", h.key, map[string]any{}); resp.StatusCode != http.StatusNotFound {
		t.Fatal(resp.Status)
	}
	if resp := h.do(t, http.MethodPut, "/v1/keys/"+k.ID+"/quota", k.Key, map[string]any{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin set quota = %s", resp.Status)
	}
}
