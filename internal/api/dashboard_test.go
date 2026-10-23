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

	"github.com/TorstenDittmann/sbx/internal/api"
	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func get(t *testing.T, url, accept string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestDashboardServesWithoutAuth(t *testing.T) {
	h := newHarness(t)
	for path, want := range map[string]string{
		"/ui/":                 "<title>sbx dashboard</title>",
		"/ui/app.js":           "streamSSE",
		"/ui/app.css":          "--accent",
		"/ui/vendor/xterm.js":  "Terminal",
		"/ui/vendor/xterm.css": ".xterm",
	} {
		res := get(t, h.srv.URL+path, "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, res.StatusCode)
		}
		body, _ := io.ReadAll(res.Body)
		if !strings.Contains(string(body), want) {
			t.Fatalf("%s: body lacks %q", path, want)
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
			t.Fatalf("%s: csp %q", path, csp)
		}
	}
	if res := get(t, h.srv.URL+"/ui/missing.js", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file: status %d", res.StatusCode)
	}
	if res := get(t, h.srv.URL+"/ui", ""); res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != "/ui/" {
		t.Fatalf("/ui: status %d location %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestRootRedirectsBrowsersToDashboard(t *testing.T) {
	h := newHarness(t)
	res := get(t, h.srv.URL+"/", "text/html,application/xhtml+xml")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/ui/" {
		t.Fatalf("browser: status %d location %q", res.StatusCode, res.Header.Get("Location"))
	}
	res = get(t, h.srv.URL+"/", "application/json")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"dashboard":"/ui/"`) {
		t.Fatalf("api client: status %d body %s", res.StatusCode, body)
	}
}

func TestDashboardDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Dashboard = false
	st, err := store.Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := core.New(cfg, st, map[string]driver.Driver{"docker": fake.New("docker")}, log)
	if err := mgr.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(mgr, log, nil).Handler())
	t.Cleanup(srv.Close)
	if res := get(t, srv.URL+"/ui/", ""); res.StatusCode != http.StatusNotFound {
		t.Fatalf("/ui/: status %d", res.StatusCode)
	}
	res := get(t, srv.URL+"/", "text/html")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || strings.Contains(string(body), "dashboard") {
		t.Fatalf("root: status %d body %s", res.StatusCode, body)
	}
}
