package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistries(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "ghcr")
	if err := os.WriteFile(secret, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sbx.yaml")
	body := "" +
		"registries:\n" +
		"  - server: https://index.docker.io/v1/\n" +
		"    username: hub\n" +
		"    password_env: HUB_TOKEN\n" +
		"  - server: ghcr.io\n" +
		"    username: bot\n" +
		"    password_file: " + secret + "\n" +
		"  - server: registry.example.com:5000\n" +
		"    identity_token: tok\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOKEN", "from-env")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	hub, ok := cfg.Registry("docker.io")
	if !ok || hub.Username != "hub" || hub.Password != "from-env" {
		t.Fatalf("%+v %v", hub, ok)
	}
	gh, ok := cfg.Registry("ghcr.io")
	if !ok || gh.Password != "from-file" {
		t.Fatalf("%+v %v", gh, ok)
	}
	if r, ok := cfg.Registry("registry.example.com:5000"); !ok || r.IdentityToken != "tok" {
		t.Fatalf("%+v %v", r, ok)
	}
	if _, ok := cfg.Registry("quay.io"); ok {
		t.Fatal("unexpected credential")
	}
}

func TestRegistryValidation(t *testing.T) {
	for name, regs := range map[string][]Registry{
		"no server":   {{Username: "a", Password: "b"}},
		"no password": {{Server: "ghcr.io", Username: "a"}},
		"duplicate":   {{Server: "ghcr.io", IdentityToken: "t"}, {Server: "ghcr.io", IdentityToken: "u"}},
	} {
		cfg := Default()
		cfg.Registries = regs
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "registries") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRegistryMissingEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbx.yaml")
	body := "registries:\n  - server: ghcr.io\n    username: bot\n    password_env: SBX_TEST_UNSET_TOKEN\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "SBX_TEST_UNSET_TOKEN") {
		t.Fatalf("%v", err)
	}
}
