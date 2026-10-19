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

func TestPreviewPublicTokenAndHost(t *testing.T) {
	cfg := config.Default()
	cfg.Network.PreviewDomain = "sbx.example.com"
	cfg.Network.PublicURL = "https://api.example.com"
	cfg.Network.AllowPublicPreviews = true
	h := newConfiguredHarness(t, cfg)

	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/previews", h.key, map[string]any{"port": 3000})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("preview %s %s", resp.Status, body)
	}
	var preview struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		PublicURL string `json:"public_url"`
		Host      string `json:"host"`
		Token     string `json:"token"`
		Public    bool   `json:"public"`
	}
	decode(t, resp, &preview)
	if preview.Public || preview.Token == "" || !strings.Contains(preview.URL, preview.ID) {
		t.Fatalf("%+v", preview)
	}
	hexID := strings.TrimPrefix(created.ID, "sbx_")
	wantHost := "3000-sbx-" + hexID + ".sbx.example.com"
	if preview.Host != wantHost || preview.PublicURL != "https://"+wantHost+"/?token="+preview.Token {
		t.Fatalf("%+v", preview)
	}

	path := "/p/" + created.ID + "/3000/health"
	resp = h.do(t, http.MethodGet, path, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("open path %s", resp.Status)
	}
	resp = h.do(t, http.MethodGet, path+"?token="+preview.Token, "", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "preview-ok" {
		t.Fatalf("token path %s %q", resp.Status, body)
	}

	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = wantHost
	req.Header.Set("X-Sbx-Preview-Token", preview.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "preview-ok" {
		t.Fatalf("host %s %q", resp.Status, body)
	}

	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/previews", h.key, map[string]any{"port": 4000, "public": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var pub struct {
		Public    bool   `json:"public"`
		PublicURL string `json:"public_url"`
	}
	decode(t, resp, &pub)
	if !pub.Public || strings.Contains(pub.PublicURL, "token=") {
		t.Fatalf("%+v", pub)
	}
	resp = h.do(t, http.MethodGet, "/p/"+created.ID+"/4000/health", "", nil)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "preview-ok" {
		t.Fatalf("public %s %q", resp.Status, body)
	}
}

func TestPreviewPublicDisabledAndKeyNetwork(t *testing.T) {
	cfg := config.Default()
	cfg.Network.AllowPublicPreviews = false
	cfg.Network.DefaultMode = "none"
	h := newConfiguredHarness(t, cfg)
	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/previews", h.key, map[string]any{"port": 9, "public": true})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("public disabled %s", resp.Status)
	}

	resp = h.do(t, http.MethodPost, "/v1/keys", h.key, map[string]any{
		"name": "agents", "scopes": []string{"sandbox"},
		"network": map[string]any{"mode": "full", "deny": []string{"example.net"}, "egress_kbps": 1000, "max_connections": 8},
	})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("key %s %s", resp.Status, body)
	}
	var key struct {
		Key     string `json:"key"`
		Network struct {
			Mode string   `json:"mode"`
			Deny []string `json:"deny"`
		} `json:"network"`
	}
	decode(t, resp, &key)
	if key.Network.Mode != "full" || len(key.Network.Deny) != 1 {
		t.Fatalf("%+v", key)
	}
	resp = h.do(t, http.MethodPost, "/v1/sandboxes", key.Key, map[string]any{"image": "alpine:3.20"})
	var sb struct {
		Network    string   `json:"network"`
		EgressDeny []string `json:"egress_deny"`
		EgressKbps int      `json:"egress_kbps"`
		MaxConns   int      `json:"max_connections"`
	}
	decode(t, resp, &sb)
	if sb.Network != "full" || sb.EgressKbps != 1000 || sb.MaxConns != 8 || len(sb.EgressDeny) != 1 {
		t.Fatalf("%+v", sb)
	}
}

func newConfiguredHarness(t *testing.T, cfg config.Config) *harness {
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
	return &harness{srv: srv, key: plain, docker: fd, store: st}
}
