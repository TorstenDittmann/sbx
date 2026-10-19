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
