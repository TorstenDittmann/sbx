package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbx.yaml")
	body := []byte("" +
		"addr: \":9090\"\n" +
		"backend: gvisor\n" +
		"docker:\n" +
		"  read_only_rootfs: false\n" +
		"  host: /tmp/docker.sock\n" +
		"limits:\n" +
		"  max_sandboxes_per_key: 3\n" +
		"auth:\n" +
		"  bootstrap_key: sbx_fromfile\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SBX_ADDR", "127.0.0.1:7070")
	t.Setenv("SBX_BOOTSTRAP_KEY", "sbx_fromenv")
	t.Setenv("SBX_READ_ONLY_ROOTFS", "true")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:7070" {
		t.Fatalf("addr %q", cfg.Addr)
	}
	if cfg.Backend != "gvisor" {
		t.Fatalf("backend %q", cfg.Backend)
	}
	if cfg.Docker.Host != "/tmp/docker.sock" {
		t.Fatalf("host %q", cfg.Docker.Host)
	}
	if !cfg.Docker.ReadOnlyRootfs {
		t.Fatal("env should turn read-only back on")
	}
	if cfg.Limits.MaxSandboxesPerKey != 3 {
		t.Fatalf("quota %d", cfg.Limits.MaxSandboxesPerKey)
	}
	if cfg.Auth.BootstrapKey != "sbx_fromenv" {
		t.Fatalf("key %q", cfg.Auth.BootstrapKey)
	}
	if cfg.Limits.DefaultTTLSeconds != 300 {
		t.Fatal("unspecified limit should stay at the default")
	}
}

func TestRejectsBadBackend(t *testing.T) {
	cfg := Default()
	cfg.Backend = "firecracker"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestEmptyFileKeepsReadOnlyDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbx.yaml")
	if err := os.WriteFile(path, []byte("log_level: debug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Docker.ReadOnlyRootfs {
		t.Fatal("read-only rootfs should default to true")
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("level %q", cfg.LogLevel)
	}
}
