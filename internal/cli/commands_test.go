package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/core"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func fakeDockerSocket(t *testing.T, runsc bool) string {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.44"})
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = io.WriteString(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/info"):
			rts := map[string]any{"runc": map[string]string{"path": "runc"}}
			if runsc {
				rts["runsc"] = map[string]string{"path": "runsc"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ServerVersion": "test", "Runtimes": rts})
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		ln.Close()
	})
	return ln.Addr().String()
}

func TestDoctor(t *testing.T) {
	sock := fakeDockerSocket(t, false)
	dir := t.TempDir()
	if err := Run([]string{"doctor", "--data-dir", dir, "--docker-host", sock}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"doctor", "--data-dir", dir, "--docker-host", sock, "--backend", "gvisor"}); err == nil {
		t.Fatal("expected gvisor failure")
	}
	if err := Run([]string{"doctor", "--data-dir", dir, "--docker-host", "tcp://127.0.0.1:1"}); err == nil {
		t.Fatal("expected socket error")
	}
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"doctor", "--data-dir", blocked, "--docker-host", sock}); err == nil {
		t.Fatal("expected data dir failure")
	}
	withRunsc := fakeDockerSocket(t, true)
	if err := Run([]string{"doctor", "--data-dir", t.TempDir(), "--docker-host", withRunsc, "--backend", "gvisor"}); err != nil {
		t.Fatal(err)
	}
}

func TestHealthcheck(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(okSrv.Close)
	if err := Run([]string{"healthcheck", "--addr", okSrv.URL}); err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(bad.Close)
	if err := Run([]string{"healthcheck", "--addr", bad.URL}); err == nil {
		t.Fatal("expected non-200")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := Run([]string{"healthcheck", "--addr", addr}); err == nil {
		t.Fatal("expected connection error")
	}
	if err := Run([]string{"healthcheck", "--addr", "http://["}); err == nil {
		t.Fatal("expected bad url")
	}
	if got := HealthURL(""); got != "http://127.0.0.1:8080/healthz" {
		t.Fatal(got)
	}
	if got := HealthURL("::"); !strings.Contains(got, "127.0.0.1") {
		t.Fatal(got)
	}
}

func TestKeysAndEnsureKey(t *testing.T) {
	if err := Run([]string{"keys"}); err == nil {
		t.Fatal("expected usage")
	}
	if err := Run([]string{"keys", "list"}); err == nil {
		t.Fatal("expected usage")
	}
	if err := Run([]string{"keys", "create"}); err == nil {
		t.Fatal("expected name")
	}
	if err := Run([]string{"no-such"}); err == nil {
		t.Fatal("expected unknown command")
	}
	dir := t.TempDir()
	if err := Run([]string{"keys", "create", "--name", "admin", "--admin", "--data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	keys, err := st.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || !keys[0].Admin() {
		t.Fatalf("%+v %v", keys, err)
	}

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	empty, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	cfg.Auth.BootstrapKey = "sbx_bootstraptest"
	plain, err := ensureKey(context.Background(), empty, cfg)
	if err != nil || plain != "" {
		t.Fatalf("bootstrap %q %v", plain, err)
	}
	again, err := ensureKey(context.Background(), empty, cfg)
	if err != nil || again != "" {
		t.Fatalf("existing %q %v", again, err)
	}

	genDir := t.TempDir()
	genCfg := config.Default()
	genCfg.DataDir = genDir
	genStore, err := store.Open(genCfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer genStore.Close()
	generated, err := ensureKey(context.Background(), genStore, genCfg)
	if err != nil || !strings.HasPrefix(generated, "sbx_") {
		t.Fatalf("generated %q %v", generated, err)
	}
	raw, err := os.ReadFile(genCfg.KeyPath())
	if err != nil || !strings.Contains(string(raw), generated) {
		t.Fatalf("key file %q %v", raw, err)
	}
}

func TestLoggerAndLoad(t *testing.T) {
	var buf bytes.Buffer
	for _, level := range []string{"debug", "info", "warn", "error", "other"} {
		log := newLogger(level, &buf)
		log.Debug("d")
		log.Info("i")
		log.Warn("w")
		log.Error("e")
	}
	if buf.Len() == 0 {
		t.Fatal("logger wrote nothing")
	}
	if _, err := loadFlags("serve", []string{"--nope"}); err == nil {
		t.Fatal("expected bad flag")
	}
	if _, err := load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected missing config")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sbx.yaml")
	if err := os.WriteFile(path, []byte("log_level: warn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFlags("doctor", []string{"--config", path, "--docker-host", "/tmp/sbx.sock", "--bootstrap-key", "sbx_flag"})
	if err != nil || cfg.LogLevel != "warn" || cfg.Docker.Host != "/tmp/sbx.sock" || cfg.Auth.BootstrapKey != "sbx_flag" {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestServeAndRunServer(t *testing.T) {
	sock := fakeDockerSocket(t, false)
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run([]string{"serve", "--addr", addr, "--data-dir", dir, "--docker-host", sock})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := Run([]string{"healthcheck", "--addr", addr}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not become healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return")
	}
	if _, err := os.Stat(filepath.Join(dir, "initial_api_key")); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	cfg.Addr = busy.Addr().String()
	mgr := core.New(cfg, st, map[string]driver.Driver{"docker": fake.New("docker")}, newLogger("error", io.Discard))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runServer(ctx, cfg, mgr, newLogger("error", io.Discard)); err == nil {
		t.Fatal("expected listen failure")
	}
}
