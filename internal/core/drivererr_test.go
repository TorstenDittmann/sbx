package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

func TestDriverErrorsBecomeOpErrors(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	if err := mgr.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	actor := coreActor(t, st)
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{driver.ErrRuntimeUnavailable, 400, "unsupported"},
		{driver.ErrImage, 400, "invalid"},
		{driver.ErrNotFound, 404, "not_found"},
		{errors.New("boom"), 502, "driver"},
	}
	for _, tc := range cases {
		fd.CreateErr = tc.err
		_, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
		var op *OpError
		if !asOp(err, &op) || op.Status != tc.status || op.Code != tc.code {
			t.Fatalf("%v -> %+v", tc.err, err)
		}
		if op.Error() == "" {
			t.Fatal("empty message")
		}
	}
	fd.CreateErr = nil
}

func TestRejectsBadInput(t *testing.T) {
	st := openStore(t)
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	cpu := 1000.0
	mem := 1
	disk := 1
	pids := 1
	ttl := 0
	idle := -1
	rejects := []CreateInput{
		{Image: "has space"},
		{Image: "alpine:3.20", Env: map[string]string{"1BAD": "x"}},
		{Image: "alpine:3.20", Env: map[string]string{"OK": "nul\x00"}},
		{Image: "alpine:3.20", Labels: map[string]string{"bad label": "x"}},
		{Image: "alpine:3.20", Labels: map[string]string{"ok": "a\nb"}},
		{Image: "alpine:3.20", Labels: map[string]string{"sbx.id": "sbx_other"}},
		{Image: "alpine:3.20", Labels: map[string]string{"sbx.managed": "false"}},
		{Image: "alpine:3.20", Network: "bridge"},
		{Image: "alpine:3.20", Backend: "kata"},
		{Template: "Not A Name"},
		{Image: "alpine:3.20", CPU: &cpu},
		{Image: "alpine:3.20", MemoryMB: &mem},
		{Image: "alpine:3.20", DiskMB: &disk},
		{Image: "alpine:3.20", PIDs: &pids},
		{Image: "alpine:3.20", TTLSeconds: &ttl},
		{Image: "alpine:3.20", IdleTimeoutSeconds: &idle},
	}
	for i, in := range rejects {
		if _, err := mgr.Create(ctx, actor, in); err == nil {
			t.Fatalf("case %d accepted %+v", i, in)
		}
	}
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Exec(ctx, actor, sb.ID, ExecInput{}); err == nil {
		t.Fatal("empty cmd")
	}
	if _, err := mgr.Exec(ctx, actor, sb.ID, ExecInput{Cmd: []string{"echo", "a\x00"}}); err == nil {
		t.Fatal("nul cmd")
	}
	if err := mgr.WriteFile(ctx, actor, sb.ID, "relative", []byte("x"), 0o644); err == nil {
		t.Fatal("relative path")
	}
	if err := mgr.WriteFile(ctx, actor, sb.ID, "/workspace/\x00", []byte("x"), 0o644); err == nil {
		t.Fatal("nul path")
	}
	if err := mgr.WriteFile(ctx, actor, sb.ID, "/workspace/../etc/passwd", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for i := 0; i < 65; i++ {
		env[fmt.Sprintf("K%02d", i)] = "v"
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Env: env}); err == nil {
		t.Fatal("expected too many env vars")
	}
}

func TestGzipArchive(t *testing.T) {
	st := openStore(t)
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("hi")
	if err := tw.WriteHeader(&tar.Header{Name: "hi.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mgr.WriteArchive(ctx, actor, sb.ID, "/workspace", buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mgr.WriteArchive(ctx, actor, sb.ID, "/workspace", []byte{0x1f, 0x8b, 0xff}); err == nil {
		t.Fatal("expected invalid gzip")
	}
}
