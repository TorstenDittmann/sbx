package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestPrepareMountsChownsVolumeRoot(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true}, cli)
	ctx := context.Background()
	if err := d.CreateVolume(ctx, "sbx-ws"); err != nil {
		t.Fatal(err)
	}
	_, err = d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_volprep", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 32,
		Network: driver.NetNone,
		Mounts:  []driver.Mount{{Source: "sbx-ws", Target: "/workspace"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	var prep *createRequest
	for i := range fd.creates {
		body := &fd.creates[i]
		if len(body.Entrypoint) > 0 && body.Entrypoint[0] == "/bin/sh" {
			prep = body
			break
		}
	}
	if prep == nil || prep.Cmd == nil {
		t.Fatal("volume prep container was not created")
	}
	if prep.User != "0:0" {
		t.Fatalf("user %s", prep.User)
	}
	if len(prep.HostConfig.CapDrop) != 0 {
		t.Fatalf("caps %+v", prep.HostConfig.CapDrop)
	}
	if len(prep.HostConfig.Binds) != 1 || prep.HostConfig.Binds[0] != "/var/lib/docker/volumes/sbx-ws/_data:/mnt" {
		t.Fatalf("binds %+v", prep.HostConfig.Binds)
	}
	script := strings.Join(*prep.Cmd, "\n")
	if !strings.Contains(script, "chown 1000:1000 /mnt") || strings.Contains(script, "chown -R") {
		t.Fatalf("script %s", script)
	}
	if !strings.Contains(script, ".sbx-owned") {
		t.Fatalf("script %s", script)
	}
}

func TestPrepareMountsChownFailure(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	fd.waitCode = 1
	fd.logs = "chown failed"
	fd.mu.Unlock()
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000}, cli)
	_, err = d.Create(context.Background(), driver.SandboxSpec{
		ID: "sbx_volfail", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 32,
		Mounts: []driver.Mount{{Source: "sbx-missing", Target: "/cache"}},
	})
	if err == nil || !strings.Contains(err.Error(), "chown") || !strings.Contains(err.Error(), "exited 1") {
		t.Fatal(err)
	}
}
