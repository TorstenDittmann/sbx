package api_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/api"
	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

type harness struct {
	srv    *httptest.Server
	key    string
	docker *fake.Driver
	store  *store.Store
	mgr    *core.Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.Limits.MaxSandboxesPerKey = 2
	st, err := store.Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	plain, hash, prefix := id.Key()
	if err := st.CreateKey(context.Background(), store.APIKey{
		ID: id.RecordID(), Name: "test", Hash: hash, Prefix: prefix,
		Scopes: []string{"admin"}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	fd := fake.New("docker")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := core.New(cfg, st, map[string]driver.Driver{"docker": fd, "gvisor": fake.New("gvisor")}, log)
	if err := mgr.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(mgr, log, []byte("openapi: 3.1.0\n")).Handler())
	t.Cleanup(srv.Close)
	return &harness{srv: srv, key: plain, docker: fd, store: st, mgr: mgr}
}

func (h *harness) do(t *testing.T, method, path, token string, body any) *http.Response {
	t.Helper()
	return h.doRaw(t, method, path, token, "application/json", body)
}

func (h *harness) doRaw(t *testing.T, method, path, token, contentType string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case string:
		r = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, h.srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func TestAuthAndPublicRoutes(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodGet, "/", "", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/healthz", "", nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/metrics", "", nil)
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "sbx_sandboxes") {
		t.Fatalf("metrics %s", raw)
	}
	resp = h.do(t, http.MethodGet, "/openapi.yaml", "", nil)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "openapi: 3.1.0") {
		t.Fatal(string(body))
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes", "sbx_not-a-real-key", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.Status)
	}
	h.docker.PingErr = io.EOF
	resp = h.do(t, http.MethodGet, "/healthz", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatal(resp.Status)
	}
}

func TestSandboxLifecycle(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"nope": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image": "alpine:3.20", "ttl_seconds": 120, "labels": map[string]string{"user": "42"},
		"env": map[string]string{"FOO": "bar"},
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s", resp.Status, b)
	}
	var created struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Backend string `json:"backend"`
		Node    string `json:"node"`
		Network string `json:"network"`
	}
	decode(t, resp, &created)
	if created.Status != "running" || created.Backend != "docker" || created.Node != "local" || created.Network != "none" {
		t.Fatalf("%+v", created)
	}

	resp = h.do(t, http.MethodGet, "/v1/sandboxes?label=user:42", h.key, nil)
	var list struct {
		Sandboxes []struct {
			ID string `json:"id"`
		} `json:"sandboxes"`
	}
	decode(t, resp, &list)
	if len(list.Sandboxes) != 1 || list.Sandboxes[0].ID != created.ID {
		t.Fatalf("%+v", list)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/exec", h.key, map[string]any{"cmd": []string{"echo", "hello"}})
	var exec struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	decode(t, resp, &exec)
	if exec.ExitCode != 0 || exec.Stdout != "hello\n" {
		t.Fatalf("%+v", exec)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/exec?stream=1", h.key, map[string]any{"cmd": []string{"echo", "streamed"}})
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "event: stdout") || !strings.Contains(string(raw), "streamed") || !strings.Contains(string(raw), "event: exit") {
		t.Fatalf("sse %s %s", resp.Status, raw)
	}

	resp = h.doRaw(t, http.MethodPut, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/a.txt", h.key, "application/octet-stream", []byte("file-body"))
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload %s %s", resp.Status, b)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/a.txt", h.key, nil)
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "file-body" {
		t.Fatalf("download %q", b)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/files?path=/workspace", h.key, nil)
	var files struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	decode(t, resp, &files)
	if len(files.Entries) != 1 || files.Entries[0].Name != "a.txt" {
		t.Fatalf("%+v", files)
	}
	resp = h.doRaw(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/files/archive?path=/workspace", h.key, "application/octet-stream", []byte("not-a-real-tar"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("archive upload %s", resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/files/archive?path=/workspace", h.key, nil)
	if resp.Header.Get("Content-Type") != "application/gzip" {
		t.Fatal(resp.Header.Get("Content-Type"))
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/timeout", h.key, map[string]any{"ttl_seconds": 600})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/pause", h.key, nil)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("pause %s %s", resp.Status, b)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/exec", h.key, map[string]any{"cmd": []string{"echo", "x"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("paused exec %s", resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/resume", h.key, nil)
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodDelete, "/v1/sandboxes/"+created.ID+"/files?path=/workspace/a.txt", h.key, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodDelete, "/v1/sandboxes/"+created.ID, h.key, nil)
	var deleted struct {
		Status string `json:"status"`
	}
	decode(t, resp, &deleted)
	if deleted.Status != "stopped" {
		t.Fatal(deleted.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{
		"image": "alpine:3.20", "network": map[string]any{"mode": "egress", "allow": []string{"pypi.org"}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
}

func TestScopesQuotaAndTemplates(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "ci", "scopes": []string{"sandbox"}})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("key %s %s", resp.Status, b)
	}
	var created struct {
		Key string `json:"key"`
	}
	decode(t, resp, &created)
	resp = h.do(t, http.MethodPost, "/v1/keys", created.Key, map[string]any{"name": "nope"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/templates", created.Key, nil)
	var tpls struct {
		Templates []struct {
			Name string `json:"name"`
		} `json:"templates"`
	}
	decode(t, resp, &tpls)
	if len(tpls.Templates) < 3 {
		t.Fatalf("%+v", tpls)
	}
	resp = h.do(t, http.MethodPost, "/v1/templates", h.key, map[string]any{"name": "tiny", "image": "alpine:3.20"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodDelete, "/v1/templates/python", h.key, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodDelete, "/v1/templates/tiny", h.key, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}

	for i := 0; i < 2; i++ {
		resp = h.do(t, http.MethodPost, "/v1/sandboxes", created.Key, map[string]any{"image": "alpine:3.20"})
		if resp.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("create %d %s %s", i, resp.Status, b)
		}
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", created.Key, map[string]any{"image": "alpine:3.20"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20", "labels": map[string]string{"owner": "admin"}})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("admin create %s %s", resp.Status, b)
	}
	var adminSB struct {
		ID string `json:"id"`
	}
	decode(t, resp, &adminSB)
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+adminSB.ID, created.Key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatal("sandbox key must not see another key's sandbox")
	}

	resp = h.do(t, http.MethodGet, "/v1/keys", h.key, nil)
	var keys struct {
		Keys []struct {
			Name       string `json:"name"`
			LastUsedAt string `json:"last_used_at"`
		} `json:"keys"`
	}
	decode(t, resp, &keys)
	if len(keys.Keys) < 2 {
		t.Fatalf("%+v", keys)
	}
	var sawUsed bool
	for _, k := range keys.Keys {
		if k.Name == "test" && k.LastUsedAt != "" {
			sawUsed = true
		}
	}
	if !sawUsed {
		t.Fatalf("admin key should have last_used_at: %+v", keys)
	}
	resp = h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "bad", "scopes": []string{"root"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{"name": "plain"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
}

func TestDegradedHealthBadModeAndArchive(t *testing.T) {
	cfg := config.Default()
	st, err := store.Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := core.New(cfg, st, map[string]driver.Driver{"gvisor": fake.New("gvisor")}, log)
	srv := httptest.NewServer(api.New(mgr, log, nil).Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatal(resp.Status)
	}

	h := newHarness(t)
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	resp = h.doRaw(t, http.MethodPut, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/a.txt&mode=999", h.key, "application/octet-stream", []byte("x"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte("not a tar, but gzip")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	resp = h.doRaw(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/files/archive?path=/workspace", h.key, "application/octet-stream", gz.Bytes())
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("gzip archive %s %s", resp.Status, b)
	}
	resp = h.do(t, http.MethodDelete, "/v1/sandboxes/"+created.ID+"/files?path=relative", h.key, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
	resp = h.do(t, http.MethodGet, "/v1/sandboxes/"+created.ID+"/files?path=", h.key, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
}
