package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlansWithoutApply(t *testing.T) {
	opt := Options{DataDir: t.TempDir(), Arch: "x86_64"}
	for _, fn := range []func(context.Context, Options) (Result, error){GVisor, Kata, Firecracker} {
		res, err := fn(context.Background(), opt)
		if err != nil {
			t.Fatal(err)
		}
		text := res.String()
		if res.Applied || !strings.Contains(text, "--apply") {
			t.Fatalf("%s", text)
		}
	}
	if _, err := GVisor(context.Background(), Options{}); err == nil {
		t.Fatal("expected missing data dir")
	}
}

func TestGVisorApplyMergesDaemonJSON(t *testing.T) {
	body := []byte("#!/bin/sh\necho runsc\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	daemon := filepath.Join(dir, "daemon.json")
	if err := os.WriteFile(daemon, []byte("{\n  \"log-level\": \"info\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := GVisor(context.Background(), Options{
		DataDir: dir, Prefix: filepath.Join(dir, "bin"), Apply: true,
		GVisorURL: srv.URL + "/runsc", DockerConfig: daemon, Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied {
		t.Fatal(res)
	}
	got, err := os.ReadFile(filepath.Join(dir, "bin", "runsc"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("%v %q", err, got)
	}
	raw, err := os.ReadFile(daemon)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	runtimes := cfg["runtimes"].(map[string]any)
	runsc := runtimes["runsc"].(map[string]any)
	if runsc["path"] == "" || cfg["log-level"] != "info" {
		t.Fatalf("%s", raw)
	}
}

func TestKataSnippetAndFirecrackerArchive(t *testing.T) {
	opt := Options{DataDir: t.TempDir(), Arch: "aarch64", DockerConfig: filepath.Join(t.TempDir(), "nope", "daemon.json")}
	res, err := Kata(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.String(), "io.containerd.kata.v2") {
		t.Fatal(res)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := []byte("firecracker-bin")
	jail := []byte("jailer-bin")
	for name, body := range map[string][]byte{"firecracker-v1.17.0-x86_64": payload, "jailer-v1.17.0-x86_64": jail} {
		hdr := &tar.Header{Name: "release/" + name, Mode: 0o755, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	kernel := bytes.Repeat([]byte("k"), 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "vmlinux") {
			_, _ = w.Write(kernel)
			return
		}
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()
	dir := t.TempDir()
	res, err = Firecracker(context.Background(), Options{
		DataDir: dir, Arch: "x86_64", Apply: true,
		FirecrackerURL: srv.URL + "/fc.tgz", KernelURL: srv.URL + "/vmlinux",
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied {
		t.Fatal(res)
	}
	fc, err := os.ReadFile(filepath.Join(dir, "firecracker", "firecracker"))
	if err != nil || !bytes.Equal(fc, payload) {
		t.Fatalf("%v %q", err, fc)
	}
	jb, err := os.ReadFile(filepath.Join(dir, "firecracker", "jailer"))
	if err != nil || !bytes.Equal(jb, jail) {
		t.Fatalf("%v %q", err, jb)
	}
	if st, err := os.Stat(filepath.Join(dir, "firecracker", "vmlinux")); err != nil || st.Size() != int64(len(kernel)) {
		t.Fatal(err)
	}
}

func TestDownloadRejectsStatusAndSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write(bytes.Repeat([]byte("a"), 32))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if _, err := download(context.Background(), srv.Client(), srv.URL+"/missing", 100); err == nil {
		t.Fatal("expected status error")
	}
	if _, err := download(context.Background(), srv.Client(), srv.URL+"/big", 8); err == nil {
		t.Fatal("expected size error")
	}
	if _, err := untarBinaries([]byte("not a tar")); err == nil {
		t.Fatal("expected archive error")
	}
}

func TestMergeRejectsBrokenJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeDockerRuntime(path, "runsc", map[string]any{"path": "runsc"}); err == nil {
		t.Fatal("expected parse error")
	}
}
