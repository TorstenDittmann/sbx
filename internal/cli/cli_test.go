package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestHealthURL(t *testing.T) {
	if got := HealthURL(":8080"); got != "http://127.0.0.1:8080/healthz" {
		t.Fatal(got)
	}
	if got := HealthURL("0.0.0.0:9090"); got != "http://127.0.0.1:9090/healthz" {
		t.Fatal(got)
	}
	if got := HealthURL("http://example.com"); got != "http://example.com/healthz" {
		t.Fatal(got)
	}
}

func TestVersionAndKeys(t *testing.T) {
	if err := Run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if err := Run(nil); err == nil {
		t.Fatal("expected usage error")
	}
	dir := t.TempDir()
	if err := Run([]string{"keys", "create", "--name", "ci", "--data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	keys, err := st.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].Name != "ci" || keys[0].Admin() {
		t.Fatalf("%+v %v", keys, err)
	}
}

func TestLoadFlagsOverride(t *testing.T) {
	cfg, err := loadFlags("serve", []string{"--addr", "127.0.0.1:1", "--backend", "gvisor", "--data-dir", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:1" || cfg.Backend != "gvisor" {
		t.Fatalf("%+v", cfg)
	}
	if _, err := loadFlags("serve", []string{"--backend", "nope"}); err == nil {
		t.Fatal("expected invalid backend")
	}
	def := config.Default()
	if !strings.HasPrefix(def.Addr, "127.0.0.1") {
		t.Fatal(def.Addr)
	}
}

func TestKeysCreateScopedAndQuota(t *testing.T) {
	dir := t.TempDir()
	args := []string{"keys", "create", "--name", "viewer", "--read-only", "--project", "acme",
		"--max-sandboxes", "5", "--max-cpu", "2.5", "--max-memory-mb", "4096", "--data-dir", dir}
	if err := Run(args); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	keys, err := st.ListKeys(context.Background())
	if err != nil || len(keys) != 1 {
		t.Fatalf("%+v %v", keys, err)
	}
	k := keys[0]
	if k.Project != "acme" || k.HasScope("sandbox") || !k.HasScope("read") ||
		k.Quota != (store.KeyQuota{MaxSandboxes: 5, MaxCPU: 2.5, MaxMemoryMB: 4096}) {
		t.Fatalf("%+v", k)
	}
	for _, bad := range [][]string{
		{"keys", "create", "--name", "x", "--admin", "--read-only", "--data-dir", dir},
		{"keys", "create", "--name", "x", "--admin", "--project", "acme", "--data-dir", dir},
		{"keys", "create", "--name", "x", "--project", "-bad", "--data-dir", dir},
		{"keys", "create", "--name", "x", "--max-cpu", "-1", "--data-dir", dir},
	} {
		if err := Run(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
