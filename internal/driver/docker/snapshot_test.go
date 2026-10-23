package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestSnapshotConfigDropsSandboxState(t *testing.T) {
	base := ImageMeta{
		Env:        []string{"PATH=/bin", "LANG=C"},
		Labels:     map[string]string{"org.opencontainers.image.title": "node"},
		Entrypoint: []string{"docker-entrypoint.sh"},
		Cmd:        []string{"node"},
		User:       "node",
		WorkingDir: "/srv",
	}
	ctn := ContainerState{
		Env:    []string{"PATH=/bin", "LANG=C", "HOME=/workspace", "TOKEN=s3cret", agentTokenEnv + "=abc"},
		Labels: map[string]string{labelManaged: "true", labelID: "sbx_1", "team": "a"},
	}
	cfg := snapshotConfig(base, ctn, "/var/lib/sbx-snapshot/snap_1")
	wantEnv := []string{"PATH=/bin", "LANG=C", "HOME=", agentTokenEnv + "=", "TOKEN="}
	if !reflect.DeepEqual(cfg.Env, wantEnv) {
		t.Fatalf("env %q", cfg.Env)
	}
	wantLabels := map[string]string{
		"org.opencontainers.image.title": "node", labelManaged: "", labelID: "", "team": "",
		labelSnapshotWorkspace: "/var/lib/sbx-snapshot/snap_1", "sbx.build": "1",
	}
	if !reflect.DeepEqual(cfg.Labels, wantLabels) {
		t.Fatalf("labels %v", cfg.Labels)
	}
	if cfg.User != "node" || cfg.WorkingDir != "/srv" || cfg.Cmd[0] != "node" || cfg.Entrypoint[0] != "docker-entrypoint.sh" {
		t.Fatalf("%+v", cfg)
	}
	// A snapshot without a workspace copy clears the label it inherited.
	base.Labels = cfg.Labels
	if got := snapshotConfig(base, ctn, "").Labels[labelSnapshotWorkspace]; got != "" {
		t.Fatalf("seed %q", got)
	}
}

func TestSnapshotAgainstFakeDaemon(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true, MaxOutputBytes: 1 << 20}, cli)
	ctx := context.Background()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_snap000000000001", Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
		Env: map[string]string{"TOKEN": "s3cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ws bytes.Buffer
	tw := tar.NewWriter(&ws)
	for _, hdr := range []*tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "./sub/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "./sub/a.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: 2},
		{Name: "./b.txt", Typeflag: tar.TypeLink, Linkname: "./sub/a.txt"},
	} {
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			_, _ = tw.Write([]byte("hi"))
		}
	}
	_ = tw.Close()
	fd.mu.Lock()
	fd.workspace = ws.Bytes()
	fd.mu.Unlock()

	tag := "sbx-template:snap_0000000000000001"
	if err := d.Snapshot(ctx, h.ID, driver.SnapshotSpec{Tag: tag, Base: "alpine:3.20", Workspace: true}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	commit := fd.commits[0]
	names := fd.builds[tag]
	fd.mu.Unlock()
	if commit.query.Get("container") != h.ID || commit.query.Get("repo") != "" || commit.query.Get("pause") != "1" {
		t.Fatalf("commit %v", commit.query)
	}
	for _, e := range commit.cfg.Env {
		if strings.Contains(e, "s3cret") {
			t.Fatalf("env leaks: %q", commit.cfg.Env)
		}
	}
	seed := "/var/lib/sbx-snapshot/snap_0000000000000001"
	want := []string{
		"Dockerfile:FROM sha256:commit\nCOPY --chown=1000:1000 workspace/ " + seed + "/\n",
		"workspace/", "workspace/sub/", "workspace/sub/a.txt", "workspace/b.txt",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("context %q", names)
	}

	// Without a workspace copy, commit tags the image directly.
	if err := d.Snapshot(ctx, h.ID, driver.SnapshotSpec{Tag: tag + "b", Base: "alpine:3.20"}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	commit = fd.commits[1]
	fd.mu.Unlock()
	if commit.query.Get("repo") != "sbx-template" || commit.query.Get("tag") != "snap_0000000000000001b" || commit.cfg.Labels[labelSnapshotWorkspace] != "" {
		t.Fatalf("commit %v %v", commit.query, commit.cfg.Labels)
	}

	// A create from the snapshot copies the seed back into /workspace.
	fd.mu.Lock()
	fd.imageLabels = map[string]map[string]string{tag: {labelSnapshotWorkspace: seed}}
	fd.mu.Unlock()
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_snap000000000002", Image: tag, CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	last := fd.execCmds[len(fd.execCmds)-1]
	fd.mu.Unlock()
	if !strings.Contains(strings.Join(last, " "), `cp -a "$1"/. /workspace/`) || last[len(last)-1] != seed {
		t.Fatalf("seed cmd %q", last)
	}
	// With a volume at /workspace the seed is left alone.
	fd.mu.Lock()
	n := len(fd.execCmds)
	fd.mu.Unlock()
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_snap000000000003", Image: tag, CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
		Mounts: []driver.Mount{{Source: "vol", Target: "/workspace"}}}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	if len(fd.execCmds) != n {
		t.Fatalf("seeded over a volume: %q", fd.execCmds[n:])
	}
	// A failing restore removes the new container.
	fd.execCode = 1
	fd.mu.Unlock()
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_snap000000000004", Image: tag, CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64}); !errors.Is(err, driver.ErrImage) {
		t.Fatalf("err = %v", err)
	}
	fd.mu.Lock()
	_, kept := fd.containers["cid-sbx_snap000000000004"]
	fd.mu.Unlock()
	if kept {
		t.Fatal("container kept after a failed restore")
	}
	if err := d.Snapshot(ctx, h.ID, driver.SnapshotSpec{Tag: "sbx-template", Base: "alpine:3.20"}); !errors.Is(err, driver.ErrConfig) {
		t.Fatalf("untagged err = %v", err)
	}

	// A snapshot of a fork drops the parent's seed, even without a new copy.
	fd.mu.Lock()
	fd.execCode = 0
	fd.mu.Unlock()
	if err := d.Snapshot(ctx, h.ID, driver.SnapshotSpec{Tag: tag + "c", Base: tag}); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	names = fd.builds[tag+"c"]
	fd.mu.Unlock()
	if len(names) != 1 || names[0] != "Dockerfile:FROM sha256:commit\nUSER root\nRUN rm -rf /var/lib/sbx-snapshot\n" {
		t.Fatalf("context %q", names)
	}

	gv := NewWithClient(Options{Name: "gvisor", Runtime: "runsc"}, cli)
	if gv.Capabilities().Snapshot {
		t.Fatal("gvisor reports snapshot")
	}
	if err := gv.Snapshot(ctx, h.ID, driver.SnapshotSpec{Tag: tag, Base: "alpine:3.20"}); !errors.Is(err, driver.ErrRuntimeUnavailable) {
		t.Fatalf("gvisor err = %v", err)
	}
}
