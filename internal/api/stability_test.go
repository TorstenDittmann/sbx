package api_test

import (
	"context"
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

func TestReadyAndRateLimit(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, http.MethodGet, "/readyz", "", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "starting") || !strings.Contains(string(body), `"docker"`) {
		t.Fatalf("before reconcile %s %s", resp.Status, body)
	}
	if err := h.mgr.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp = h.do(t, http.MethodGet, "/readyz", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready %s", resp.Status)
	}

	cfg := config.Default()
	cfg.Limits.RequestsPerMinute = 2
	cfg.Limits.MaxUploadBytes = 8
	limited := harnessWithConfig(t, cfg)
	var limitedN int
	for i := 0; i < 4; i++ {
		resp = limited.do(t, http.MethodGet, "/v1/sandboxes", limited.key, nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limitedN++
		}
	}
	if limitedN == 0 {
		t.Fatal("expected a rate limit")
	}

	req, err := http.NewRequest(http.MethodPost, limited.srv.URL+"/v1/sandboxes", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+limited.key)
	req.ContentLength = 10
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit %s", resp.Status)
	}
}

func TestBodyLimitsNameTheSetting(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxUploadBytes = 1024
	cfg.Limits.MaxArchiveBytes = 2048
	h := harnessWithConfig(t, cfg)
	big := strings.Repeat("x", 1500)

	// Content-Length over the archive limit is refused before the handler.
	resp := h.doRaw(t, http.MethodPost, "/v1/sandboxes/sbx_missing/files/archive?path=/workspace", h.key, "application/octet-stream", strings.Repeat("x", 4096))
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body, "2 KiB") || !strings.Contains(body, "max_archive_bytes") {
		t.Fatal(resp.Status, body)
	}

	// An archive between the two limits gets past the body check.
	resp = h.doRaw(t, http.MethodPost, "/v1/sandboxes/sbx_missing/files/archive?path=/workspace", h.key, "application/octet-stream", big)
	body = readBody(t, resp)
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatal(resp.Status, body)
	}

	resp = h.doRaw(t, http.MethodPut, "/v1/sandboxes/sbx_missing/files/content?path=/workspace/a", h.key, "application/octet-stream", big)
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body, "1 KiB") || !strings.Contains(body, "max_upload_bytes") {
		t.Fatal(resp.Status, body)
	}

	// A chunked body has no Content-Length; the handler enforces the limit.
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/sandboxes/sbx_missing/files/archive?path=/workspace", io.MultiReader(strings.NewReader(strings.Repeat("x", 4096))))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(body, "max_archive_bytes") {
		t.Fatal(resp.Status, body)
	}
}

func harnessWithConfig(t *testing.T, cfg config.Config) *harness {
	t.Helper()
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
	mgr := core.New(cfg, st, map[string]driver.Driver{"docker": fd}, log)
	srv := httptest.NewServer(api.New(mgr, log, nil).Handler())
	t.Cleanup(srv.Close)
	return &harness{srv: srv, key: plain, docker: fd, store: st, mgr: mgr}
}
