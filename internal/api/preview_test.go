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
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(h.srv.URL + path + "?token=" + preview.Token + "&x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != path+"?x=1" {
		t.Fatalf("token path %s location %q", resp.Status, resp.Header.Get("Location"))
	}
	var tokenCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "sbx_preview" {
			tokenCookie = c
		}
	}
	if tokenCookie == nil || tokenCookie.Value != preview.Token || !tokenCookie.HttpOnly || !tokenCookie.Secure || tokenCookie.Path != "/p/"+created.ID+"/3000/" {
		t.Fatalf("token cookie %+v", tokenCookie)
	}
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "app=1; sbx_preview=wrong; sbx_preview="+preview.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "preview-ok" {
		t.Fatalf("token cookie path %s %q", resp.Status, body)
	}
	if got := resp.Header.Get("X-Fake-Cookie"); got != "" {
		t.Fatalf("path preview forwarded cookies %q", got)
	}

	req, err = http.NewRequest(http.MethodGet, h.srv.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = wantHost
	req.Header.Set("X-Sbx-Preview-Token", preview.Token)
	req.Header.Set("Cookie", "session=abc; sbx_preview=x")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "preview-ok" {
		t.Fatalf("host %s %q", resp.Status, body)
	}
	if got := resp.Header.Get("X-Fake-Cookie"); got != "session=abc" {
		t.Fatalf("host preview cookies %q, want the app cookie without the token", got)
	}

	// X-Forwarded-Host is ignored unless the operator trusts the proxy.
	req, err = http.NewRequest(http.MethodGet, h.srv.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Host", wantHost)
	req.Header.Set("X-Sbx-Preview-Token", preview.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) == "preview-ok" {
		t.Fatal("untrusted X-Forwarded-Host routed to a preview")
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

func TestPreviewTrustedForwardedHost(t *testing.T) {
	cfg := config.Default()
	cfg.Network.PreviewDomain = "sbx.example.com"
	cfg.Network.TrustForwardedHost = true
	h := newConfiguredHarness(t, cfg)
	var created struct {
		ID string `json:"id"`
	}
	decode(t, h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"}), &created)
	var preview struct {
		Token string `json:"token"`
	}
	decode(t, h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/previews", h.key, map[string]any{"port": 3000}), &preview)
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Host", "3000-sbx-"+strings.TrimPrefix(created.ID, "sbx_")+".sbx.example.com")
	req.Header.Set("X-Sbx-Preview-Token", preview.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "preview-ok" {
		t.Fatalf("trusted forwarded host %s %q", resp.Status, body)
	}
}
