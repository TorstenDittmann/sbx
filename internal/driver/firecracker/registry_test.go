package firecracker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

// fakeRegistryDaemon answers a pull only when X-Registry-Auth carries bot.
func fakeRegistryDaemon(t *testing.T) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var users []string
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": "1.44"})
	})
	mux.HandleFunc("/v1.44/images/create", func(w http.ResponseWriter, r *http.Request) {
		var auth struct {
			Username string `json:"username"`
		}
		if raw, err := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth")); err == nil {
			_ = json.Unmarshal(raw, &auth)
		}
		mu.Lock()
		users = append(users, auth.Username)
		mu.Unlock()
		if auth.Username != "bot" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "unauthorized"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "pulled"})
	})
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), users...)
	}
}

func TestForcePullGuardsRootfsCache(t *testing.T) {
	sock, users := fakeRegistryDaemon(t)
	dir := t.TempDir()
	agentBin := filepath.Join(dir, "sbx-agent")
	if err := os.WriteFile(agentBin, []byte("agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA(agentBin)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	const image = "ghcr.io/acme/app:1"
	// Another key's earlier create left the rootfs in the cache.
	if err := os.WriteFile(filepath.Join(cache, cacheKey(image, sum, 32)+".ext4"), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RootSpec{Image: image, Agent: agentBin, DiskMB: 32, ForcePull: true}

	denied := spec
	denied.Dest = filepath.Join(dir, "denied.ext4")
	denied.Registry = &driver.RegistryAuth{Server: "ghcr.io", Username: "other", Password: "x"}
	if err := buildRootfs(context.Background(), sock, cache, denied); !errors.Is(err, driver.ErrImage) {
		t.Fatalf("expected image error, got %v", err)
	}
	if _, err := os.Stat(denied.Dest); !os.IsNotExist(err) {
		t.Fatal("cached rootfs copied without registry access")
	}

	allowed := spec
	allowed.Dest = filepath.Join(dir, "allowed.ext4")
	allowed.Registry = &driver.RegistryAuth{Server: "ghcr.io", Username: "bot", Password: "s3cret"}
	if err := buildRootfs(context.Background(), sock, cache, allowed); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(allowed.Dest); err != nil || string(raw) != "cached" {
		t.Fatal(string(raw), err)
	}
	if got := users(); len(got) != 2 || got[0] != "other" || got[1] != "bot" {
		t.Fatalf("pulls %v", got)
	}
}
