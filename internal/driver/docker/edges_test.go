package docker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestNetworkCreatePaths(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	fd.mu.Lock()
	fd.network = true
	fd.mu.Unlock()
	bridge, err := cli.EnsureSandboxNetwork(ctx, false)
	if err != nil || bridge != "sbx0" {
		t.Fatalf("existing %s %v", bridge, err)
	}
	fd.mu.Lock()
	if fd.netCreates != 0 {
		t.Fatalf("creates %d", fd.netCreates)
	}
	fd.network = false
	fd.netStatus = http.StatusConflict
	fd.mu.Unlock()
	if bridge, err = cli.EnsureSandboxNetwork(ctx, false); err != nil || bridge != "sbx0" {
		t.Fatalf("conflict %s %v", bridge, err)
	}

	fd.mu.Lock()
	fd.network = false
	fd.netStatus = http.StatusInternalServerError
	fd.mu.Unlock()
	if _, err := cli.EnsureSandboxNetwork(ctx, false); err == nil || !strings.Contains(err.Error(), "network create failed") {
		t.Fatal(err)
	}
	if (&StatusError{Status: http.StatusTeapot}).Error() == "" {
		t.Fatal("empty status error")
	}
}

func TestFullNetworkFailClosedAndAttached(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	blocked := NewWithClient(Options{
		Name: "docker", UID: 1000, GID: 1000,
		ScriptRunner: func(context.Context, string) error { return errors.New("denied") },
	}, cli)
	if _, err := blocked.Create(ctx, driver.SandboxSpec{
		ID: "sbx_fullblocked0001", Image: "alpine:3.20", CPU: 0.25, MemoryMB: 64, DiskMB: 16, PIDs: 32,
		Network: driver.NetFull,
	}); err == nil || !strings.Contains(err.Error(), "refusing network=full") {
		t.Fatal(err)
	}
	fd.mu.Lock()
	if len(fd.containers) != 0 {
		t.Fatalf("sandbox created despite firewall failure: %d", len(fd.containers))
	}
	fd.mu.Unlock()

	prev := runScriptHook
	t.Cleanup(func() { runScriptHook = prev })
	runScriptHook = func(context.Context, string) error { return nil }
	ok := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true}, cli)
	if _, err := ok.Create(ctx, driver.SandboxSpec{
		ID: "sbx_fullok000000001", Image: "alpine:3.20", CPU: 0.25, MemoryMB: 64, DiskMB: 16, PIDs: 32,
		Network: driver.NetFull,
	}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	if fd.created.HostConfig.NetworkMode != sandboxNetwork {
		t.Fatalf("mode %s", fd.created.HostConfig.NetworkMode)
	}
	fd.mu.Unlock()
}

func TestExecAndFileFailures(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20, NoEgressFilter: true}, cli)
	ctx := context.Background()
	h, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_edges000000001", Image: "alpine:3.20", CPU: 0.25, MemoryMB: 64, DiskMB: 16, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ctx, h.ID, driver.ExecRequest{}); err == nil {
		t.Fatal("expected empty cmd")
	}
	fd.mu.Lock()
	fd.execCode = 1
	fd.mu.Unlock()
	if _, err := d.ListFiles(ctx, h.ID, "/workspace"); err == nil || !strings.Contains(err.Error(), "warn") {
		t.Fatal(err)
	}
	if _, err := d.ReadFile(ctx, h.ID, "/workspace/a.txt", 0); err == nil {
		t.Fatal("expected read failure")
	}
	if err := d.WriteFile(ctx, h.ID, "/workspace/a.txt", []byte("abc"), 0); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Fatal(err)
	}
	if err := d.RemoveFile(ctx, h.ID, "/workspace/a.txt"); err == nil {
		t.Fatal("expected rm failure")
	}
	fd.mu.Lock()
	fd.execCode = 0
	fd.mu.Unlock()
	if _, err := d.ReadFile(ctx, h.ID, "/workspace/a.txt", 4); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal(err)
	}
	if err := d.WriteFile(ctx, h.ID, "/workspace/a.txt", []byte("abc"), 0); err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{"created", "exited", "dead", "removing", "weird"} {
		fd.setStatus(h.ID, status)
		st, err := d.Inspect(ctx, h.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := driver.StateStopped
		if status == "weird" {
			want = driver.StateUnknown
		}
		if st.State != want {
			t.Fatalf("%s -> %s", status, st.State)
		}
	}
}

func TestPullAndStartFailures(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, NoEgressFilter: true}, cli)
	ctx := context.Background()
	spec := driver.SandboxSpec{ID: "sbx_pullfail000001", Image: "alpine:3.20", CPU: 0.25, MemoryMB: 64, DiskMB: 16, PIDs: 32}
	fd.mu.Lock()
	fd.failPull = true
	fd.mu.Unlock()
	if _, err := d.Create(ctx, spec); !errors.Is(err, driver.ErrImage) {
		t.Fatal(err)
	}
	fd.mu.Lock()
	fd.failPull = false
	fd.pullGhost = true
	fd.mu.Unlock()
	spec.ID = "sbx_pullghost00001"
	if _, err := d.Create(ctx, spec); !errors.Is(err, driver.ErrImage) {
		t.Fatal(err)
	}
	fd.mu.Lock()
	fd.pullGhost = false
	fd.failStart = true
	fd.mu.Unlock()
	spec.ID = "sbx_startfail00001"
	if _, err := d.Create(ctx, spec); err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatal(err)
	}
	fd.mu.Lock()
	if len(fd.containers) != 0 {
		t.Fatalf("leftover containers %d", len(fd.containers))
	}
	fd.mu.Unlock()
}
