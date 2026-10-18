package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apispec "github.com/TorstenDittmann/sbx/api"
	"github.com/TorstenDittmann/sbx/internal/api"
	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/docker"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestHTTPQuickstart(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Limits.DefaultMemoryMB = 64
	cfg.Limits.DefaultDiskMB = 32
	cfg.Limits.DefaultCPU = 0.5
	st, err := store.Open(filepath.Join(cfg.DataDir, "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	plain := "sbx_e2equickstartkey00000000000000"
	if err := st.CreateKey(context.Background(), store.APIKey{
		ID: id.RecordID(), Name: "e2e", Hash: id.Hash(plain), Prefix: plain[:12],
		Scopes: []string{"admin"}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	base := docker.Options{Host: cfg.Docker.Host, ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: cfg.Limits.MaxOutputBytes}
	dockOpt := base
	dockOpt.Name = "docker"
	gOpt := base
	gOpt.Name = "gvisor"
	gOpt.Runtime = "runsc"
	dock, err := docker.New(dockOpt)
	if err != nil {
		t.Fatal(err)
	}
	gvis, err := docker.New(gOpt)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := core.New(cfg, st, map[string]driver.Driver{"docker": dock, "gvisor": gvis}, log)
	if err := mgr.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(api.New(mgr, log, apispec.OpenAPI).Handler())
	t.Cleanup(func() {
		ctx := context.Background()
		list, _ := dock.List(ctx)
		for _, sb := range list {
			_ = dock.Delete(ctx, sb.ID)
		}
		ts.Close()
	})

	createBody := []byte(`{"template":"base","ttl_seconds":180,"env":{"FOO":"bar"}}`)
	resp := call(t, ts, http.MethodPost, "/v1/sandboxes", plain, "application/json", createBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s %s", resp.Status, resp.body)
	}
	var created struct {
		ID     string `json:"id"`
		Image  string `json:"image"`
		Status string `json:"status"`
	}
	mustDecode(t, resp.body, &created)
	if created.Status != "running" || created.Image != "alpine:3.20" {
		t.Fatalf("%+v", created)
	}
	t.Cleanup(func() {
		_ = call(t, ts, http.MethodDelete, "/v1/sandboxes/"+created.ID, plain, "", nil)
	})

	execBody := []byte(`{"cmd":["sh","-c","echo $FOO"],"timeout_seconds":20}`)
	resp = call(t, ts, http.MethodPost, "/v1/sandboxes/"+created.ID+"/exec", plain, "application/json", execBody)
	var result struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	mustDecode(t, resp.body, &result)
	if resp.StatusCode != 200 || result.ExitCode != 0 || result.Stdout != "bar\n" {
		t.Fatalf("exec %s %+v", resp.Status, result)
	}

	resp = call(t, ts, http.MethodPut, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/hello.txt", plain, "application/octet-stream", []byte("hello from sbx\n"))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("upload %s %s", resp.Status, resp.body)
	}
	resp = call(t, ts, http.MethodGet, "/v1/sandboxes/"+created.ID+"/files/content?path=/workspace/hello.txt", plain, "", nil)
	if string(resp.body) != "hello from sbx\n" {
		t.Fatalf("download %q", resp.body)
	}
	resp = call(t, ts, http.MethodPost, "/v1/sandboxes/"+created.ID+"/exec?stream=1", plain, "application/json", []byte(`{"cmd":["cat","/workspace/hello.txt"],"timeout_seconds":20}`))
	if !strings.Contains(string(resp.body), "hello from sbx") || !strings.Contains(string(resp.body), "event: exit") {
		t.Fatalf("stream %s", resp.body)
	}
	resp = call(t, ts, http.MethodDelete, "/v1/sandboxes/"+created.ID, plain, "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(resp.body), `"status":"stopped"`) && !strings.Contains(string(resp.body), `"status": "stopped"`) {
		t.Fatalf("delete %s %s", resp.Status, resp.body)
	}
}

type httpResult struct {
	StatusCode int
	Status     string
	body       []byte
}

func call(t *testing.T, ts *httptest.Server, method, path, key, contentType string, body []byte) httpResult {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{StatusCode: resp.StatusCode, Status: resp.Status, body: b}
}

func mustDecode(t *testing.T, body []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("%v body %s", err, body)
	}
}
