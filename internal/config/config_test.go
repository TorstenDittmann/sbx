package config

import (
	"os"
	"path/filepath"
	"strings"
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
	if cfg.Kata.Runtime != "kata" || cfg.Kata.RuntimeType != "io.containerd.kata.v2" {
		t.Fatalf("kata defaults %+v", cfg.Kata)
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
	if cfg.Network.DefaultMode != "none" || !cfg.Network.BlockPrivate || cfg.Network.IPv6 != "auto" {
		t.Fatalf("network defaults %+v", cfg.Network)
	}
}

func TestNetworkConfigFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbx.yaml")
	body := []byte("" +
		"network:\n" +
		"  default_mode: full\n" +
		"  block_private: false\n" +
		"  private_allow:\n" +
		"    - 10.1.0.0/16\n" +
		"  dns:\n" +
		"    - 9.9.9.9\n" +
		"  ipv6: \"on\"\n" +
		"  egress_deny:\n" +
		"    - example.net\n" +
		"  preview_domain: sbx.example.com\n" +
		"  public_url: https://api.example.com\n" +
		"docker:\n" +
		"  sandbox_root: true\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SBX_NETWORK_MODE", "allow")
	t.Setenv("SBX_EGRESS_ALLOW", "1.1.1.1/32,example.com")
	t.Setenv("SBX_BLOCK_METADATA", "false")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.DefaultMode != "allow" || cfg.Network.BlockPrivate || cfg.Network.BlockMetadata {
		t.Fatalf("%+v", cfg.Network)
	}
	if len(cfg.Network.EgressAllow) != 2 || cfg.Network.DNS[0] != "9.9.9.9" || cfg.Network.IPv6 != "on" {
		t.Fatalf("%+v", cfg.Network)
	}
	if cfg.Network.PreviewDomain != "sbx.example.com" || !cfg.Docker.SandboxRoot {
		t.Fatalf("%+v %+v", cfg.Network, cfg.Docker)
	}
	cfg.Network.DefaultMode = "weird"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid mode")
	}
}

func TestValidateEdges(t *testing.T) {
	cases := []func(c *Config){
		func(c *Config) { c.Addr = "" },
		func(c *Config) { c.DataDir = "" },
		func(c *Config) { c.Docker.SandboxUID = 0 },
		func(c *Config) { c.Limits.DefaultTTLSeconds = c.Limits.MaxTTLSeconds + 1 },
		func(c *Config) { c.Limits.MaxSandboxesPerKey = 0 },
		func(c *Config) { c.Limits.DefaultCPU = c.Limits.MaxCPU + 1 },
		func(c *Config) { c.Limits.DefaultMemoryMB = 1 },
		func(c *Config) { c.Limits.DefaultDiskMB = 1 },
		func(c *Config) { c.Limits.DefaultPIDs = 1 },
		func(c *Config) { c.Limits.DefaultExecSeconds = c.Limits.MaxExecSeconds + 1 },
		func(c *Config) { c.Limits.MaxOutputBytes = 1 },
		func(c *Config) { c.Limits.ReaperIntervalSeconds = 0 },
		func(c *Config) { c.LogLevel = "verbose" },
	}
	for i, mutate := range cases {
		cfg := Default()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	t.Setenv("SBX_MAX_UPLOAD_BYTES", "4096")
	t.Setenv("SBX_SANDBOX_UID", "2000")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxUploadBytes != 4096 || cfg.Docker.SandboxUID != 2000 || cfg.Limits.DefaultTTLSeconds == 0 {
		t.Fatalf("%+v", cfg)
	}
	if Default().KeyPath() == "" || Default().DBPath() == "" {
		t.Fatal("paths")
	}
}

func TestRejectsBadBackend(t *testing.T) {
	cfg := Default()
	cfg.Backend = "kubernetes"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error")
	}
	cfg = Default()
	cfg.Backend = "firecracker"
	cfg.Firecracker.Jailer = "/usr/bin/jailer"
	cfg.Firecracker.UID = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected jailer uid error")
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

func TestEnvCoversEveryFileKey(t *testing.T) {
	keys := EnvKeys()
	seen := map[string]bool{}
	for _, key := range keys {
		name := EnvName(key)
		if seen[name] {
			t.Fatalf("duplicate env name %s", name)
		}
		seen[name] = true
	}
	for _, want := range []string{"addr", "docker.host", "limits.max_cpu", "network.dns", "builds.timeout_seconds"} {
		if !seen[EnvName(want)] {
			t.Fatalf("missing %s", want)
		}
	}
	for old, canonical := range legacyEnv {
		if seen[old] {
			t.Fatalf("legacy %s collides with a canonical name", old)
		}
		if !seen[canonical] {
			t.Fatalf("legacy %s points at unknown %s", old, canonical)
		}
	}
}

func TestCanonicalEnvNames(t *testing.T) {
	t.Setenv("SBX_LIMITS_MAX_CPU", "8")
	t.Setenv("SBX_LIMITS_DEFAULT_CPU", "2.5")
	t.Setenv("SBX_LIMITS_DEFAULT_PIDS", "512")
	t.Setenv("SBX_LIMITS_CREATING_TIMEOUT_SECONDS", "60")
	t.Setenv("SBX_NETWORK_DEFAULT_MODE", "full")
	t.Setenv("SBX_NETWORK_DNS", "9.9.9.9, 1.0.0.1")
	t.Setenv("SBX_AUTH_BOOTSTRAP_KEY", "sbx_canonical")
	t.Setenv("SBX_BUILDS_TIMEOUT_SECONDS", "30")
	t.Setenv("SBX_DOCKER_READ_ONLY_ROOTFS", "false")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxCPU != 8 || cfg.Limits.DefaultCPU != 2.5 || cfg.Limits.DefaultPIDs != 512 || cfg.Limits.CreatingTimeoutSeconds != 60 {
		t.Fatalf("%+v", cfg.Limits)
	}
	if cfg.Network.DefaultMode != "full" || len(cfg.Network.DNS) != 2 || cfg.Network.DNS[1] != "1.0.0.1" {
		t.Fatalf("%+v", cfg.Network)
	}
	if cfg.Auth.BootstrapKey != "sbx_canonical" || cfg.Builds.TimeoutSeconds != 30 || cfg.Docker.ReadOnlyRootfs {
		t.Fatalf("%+v %+v %+v", cfg.Auth, cfg.Builds, cfg.Docker)
	}
}

func TestCanonicalEnvBeatsLegacy(t *testing.T) {
	t.Setenv("SBX_NETWORK_MODE", "none")
	t.Setenv("SBX_NETWORK_DEFAULT_MODE", "full")
	t.Setenv("SBX_BOOTSTRAP_KEY", "sbx_legacy")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.DefaultMode != "full" {
		t.Fatalf("mode %q", cfg.Network.DefaultMode)
	}
	if cfg.Auth.BootstrapKey != "sbx_legacy" {
		t.Fatalf("legacy name should still work, got %q", cfg.Auth.BootstrapKey)
	}
}

func TestEnvOnlyUntouchedSectionsKeepDefaults(t *testing.T) {
	t.Setenv("SBX_LIMITS_MAX_PIDS", "8192")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Limits.MaxPIDs = 8192
	if cfg.Limits != want.Limits || cfg.Docker != want.Docker || cfg.Kata != want.Kata {
		t.Fatalf("%+v", cfg)
	}
}

func TestBadEnvValueFails(t *testing.T) {
	for name, val := range map[string]string{
		"SBX_DEFAULT_TTL_SECONDS":   "nope",
		"SBX_LIMITS_MAX_CPU":        "lots",
		"SBX_NETWORK_BLOCK_PRIVATE": "maybe",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, val)
			_, err := Load("")
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("want error naming %s, got %v", name, err)
			}
		})
	}
}

func TestConfigDocsListEveryEnvName(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "website", "src", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, key := range EnvKeys() {
		if !strings.Contains(doc, "`"+EnvName(key)+"`") {
			t.Errorf("configuration.md does not list %s (%s)", EnvName(key), key)
		}
	}
}
