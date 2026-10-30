package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
)

// Path previews share the API and dashboard origin, so the proxy must not
// let sandbox code register a service worker there, set cookies there, or
// see the caller's API key.
func TestPathPreviewCannotReachTheAPIOrigin(t *testing.T) {
	cfg := config.Default()
	cfg.Network.PreviewDomain = "sbx.example.com"
	cfg.Network.AllowPublicPreviews = true
	h := newConfiguredHarness(t, cfg)

	resp := h.do(t, http.MethodPost, "/v1/sandboxes", h.key, map[string]any{"image": "alpine:3.20"})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	resp = h.do(t, http.MethodPost, "/v1/sandboxes/"+created.ID+"/previews", h.key, map[string]any{"port": 3000, "public": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(resp.Status)
	}
	var preview struct {
		ID string `json:"id"`
	}
	decode(t, resp, &preview)

	get := func(url, host string, header map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, h.srv.URL+url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp
	}

	path := "/p/" + created.ID + "/3000/sw.js"
	resp = get(path, "", map[string]string{"Referer": h.srv.URL + "/p/x/?access_token=" + h.key})
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	if got := resp.Header.Get("Service-Worker-Allowed"); got != "" {
		t.Fatalf("path preview passed Service-Worker-Allowed %q", got)
	}
	if got := resp.Header.Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("path preview passed Set-Cookie %q", got)
	}
	if got := resp.Header.Get("X-Fake-Referer"); got != "" {
		t.Fatalf("sandbox saw the Referer %q", got)
	}
	resp = get(path, "", map[string]string{"Service-Worker": "script"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("service worker script on a path preview: %s", resp.Status)
	}

	// The owner route refuses a browser navigation that carries the API key
	// in the URL, and still serves API clients.
	owner := "/v1/sandboxes/" + created.ID + "/previews/" + preview.ID + "/health?access_token=" + h.key
	if resp = get(owner, "", map[string]string{"Sec-Fetch-Mode": "navigate"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("browser navigation with access_token: %s", resp.Status)
	}
	if resp = get(owner, "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("api client with access_token: %s", resp.Status)
	}
	host := "3000-sbx-" + strings.TrimPrefix(created.ID, "sbx_") + ".sbx.example.com"
	nav := map[string]string{"Sec-Fetch-Mode": "navigate"}
	if resp = get(path+"?access_token="+h.key, "", nav); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("browser navigation to a path preview with access_token: %s", resp.Status)
	}
	if resp = get("/?access_token="+h.key, host, nav); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("browser navigation to a host preview with access_token: %s", resp.Status)
	}

	// Host previews have their own origin and keep their cookies.
	resp = get("/sw.js", host, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	if got := resp.Header.Get("Set-Cookie"); got == "" {
		t.Fatal("host preview dropped the app's Set-Cookie")
	}
	if got := resp.Header.Get("Service-Worker-Allowed"); got != "" {
		t.Fatalf("host preview passed Service-Worker-Allowed %q", got)
	}
}
