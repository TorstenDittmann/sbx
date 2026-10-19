package kata

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/docker"
)

type runtimeEntry struct {
	Path        string
	RuntimeType string
}

func serveDocker(t *testing.T, runtimes map[string]runtimeEntry) string {
	t.Helper()
	ln, err := net.Listen("unix", t.TempDir()+"/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.44"})
	})
	mux.HandleFunc("/v1.44/info", func(w http.ResponseWriter, _ *http.Request) {
		raw := map[string]any{}
		for name, rt := range runtimes {
			raw[name] = map[string]string{"path": rt.Path, "runtimeType": rt.RuntimeType}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ServerVersion": "test", "Runtimes": raw})
	})
	mux.HandleFunc("/v1.44/_ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/v1.44/images/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Env": []string{"PATH=/bin"}}})
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/v1.44/containers/create", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "ctr-kata"})
	})
	mux.HandleFunc("/v1.44/containers/ctr-kata/start", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestCheckAndCreate(t *testing.T) {
	runtimes := map[string]runtimeEntry{}
	sock := serveDocker(t, runtimes)
	d, err := New(Options{Host: sock, SkipKVM: true, UID: 1000, GID: 1000, ReadOnlyRootfs: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "no \"kata\" runtime") {
		t.Fatalf("missing runtime: %v", err)
	}
	info := d.Report(context.Background())
	if info.Available || info.Isolation != "kata" || info.Name != "kata" {
		t.Fatalf("%+v", info)
	}

	runtimes["kata"] = runtimeEntry{Path: "/usr/bin/runc", RuntimeType: "io.containerd.runc.v2"}
	if err := d.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "io.containerd.kata.v2") {
		t.Fatalf("wrong type: %v", err)
	}

	runtimes["kata"] = runtimeEntry{Path: "/opt/kata/bin/containerd-shim-kata-v2", RuntimeType: "io.containerd.kata.v2"}
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := d.Report(context.Background())
	if !ready.Available || !ready.Exec || ready.Snapshot {
		t.Fatalf("%+v", ready)
	}
	h, err := d.Create(context.Background(), driver.SandboxSpec{
		ID: "sbx_kata0000000001", Image: "alpine:3.20", CPU: 1, MemoryMB: 128, DiskMB: 256, PIDs: 64,
	})
	if err != nil || h.RuntimeName != "kata" {
		t.Fatalf("%+v %v", h, err)
	}
	if d.Capabilities().IsolationLevel != "kata" {
		t.Fatal(d.Capabilities())
	}
}

func TestLegacyKataPath(t *testing.T) {
	runtimes := map[string]runtimeEntry{
		"kata-qemu": {Path: "/usr/bin/kata-runtime"},
	}
	sock := serveDocker(t, runtimes)
	inner, err := docker.New(docker.Options{Host: sock, Name: "kata", Runtime: "kata-qemu", UID: 1000, GID: 1000})
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithDocker(inner, "kata-qemu", "io.containerd.kata.v2", true)
	if err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}
