package core

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestSweepTTLIdleOrphanAndQuota(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxSandboxesPerKey = 1
	cfg.Limits.CreatingTimeoutSeconds = 30
	cfg.Limits.RetainStoppedSeconds = 60
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	cur := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	mgr.SetClock(func() time.Time { return cur })
	ctx := context.Background()
	actor := coreActor(t, st)

	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", TTLSeconds: intPtr(60)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"}); err == nil {
		t.Fatal("expected quota error")
	} else {
		var op *OpError
		if !asOp(err, &op) || op.Status != 429 {
			t.Fatal(err)
		}
	}

	cur = cur.Add(61 * time.Second)
	n, err := mgr.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("reaped %d %v", n, err)
	}
	got, err := st.GetSandbox(ctx, sb.ID)
	if err != nil || got.Status != "stopped" || got.Error != "ttl" {
		t.Fatalf("after ttl %+v %v", got, err)
	}

	idle, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", TTLSeconds: intPtr(600), IdleTimeoutSeconds: intPtr(30)})
	if err != nil {
		t.Fatal(err)
	}
	cur = cur.Add(31 * time.Second)
	if n, err = mgr.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("idle reaped %d %v", n, err)
	}
	got, err = st.GetSandbox(ctx, idle.ID)
	if err != nil || got.Error != "idle" {
		t.Fatalf("idle %+v %v", got, err)
	}

	stuck := store.Sandbox{
		ID: "sbx_stuck000000000", Image: "alpine:3.20", Backend: "docker", Status: "creating",
		CPU: 1, MemoryMB: 128, DiskMB: 64, PIDs: 32, Network: "none", OwnerKeyID: actor.KeyID,
		CreatedAt: cur.Add(-time.Minute), ExpiresAt: cur.Add(time.Hour), LastActivityAt: cur, ContainerID: "sbx_stuck000000000",
	}
	fd.Seed(driver.Status{ID: stuck.ContainerID, SandboxID: stuck.ID, State: driver.StateRunning, Image: stuck.Image})
	if err := st.InsertSandbox(ctx, stuck); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetSandbox(ctx, stuck.ID)
	if err != nil || got.Status != "stopped" || got.Error != "create timed out" {
		t.Fatalf("stuck %+v %v", got, err)
	}

	fd.Seed(driver.Status{ID: "orphan-1", SandboxID: "sbx_missing0000000", State: driver.StateRunning})
	if _, err := mgr.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fd.Inspect(ctx, "orphan-1"); err != driver.ErrNotFound {
		t.Fatal("orphan should be deleted", err)
	}

	live, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", TTLSeconds: intPtr(600)})
	if err != nil {
		t.Fatal(err)
	}
	if err := fd.Delete(ctx, live.ContainerID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetSandbox(ctx, live.ID)
	if err != nil || got.Status != "stopped" || got.Error != "lost" {
		t.Fatalf("reconcile %+v %v", got, err)
	}

	cur = cur.Add(2 * time.Minute)
	if _, err := mgr.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSandbox(ctx, sb.ID); err != store.ErrNotFound {
		t.Fatal("expected pruned row", err)
	}
}

func TestVisibilityAndRejects(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd, "gvisor": fake.New("gvisor")}, discard())
	ctx := context.Background()
	owner := coreActor(t, st)
	other := store.APIKey{ID: id.RecordID(), Name: "other", Hash: "abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcab12", Prefix: "sbx_other", Scopes: []string{"sandbox"}, CreatedAt: time.Now()}
	if err := st.CreateKey(ctx, other); err != nil {
		t.Fatal(err)
	}
	sb, err := mgr.Create(ctx, owner, CreateInput{Template: "nope"})
	if err == nil {
		t.Fatal("expected missing template")
	}
	if err := mgr.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	sb, err = mgr.Create(ctx, owner, CreateInput{Template: "python", Labels: map[string]string{"user": "7"}})
	if err != nil || sb.Image != "python:3.12-slim" || sb.Backend != "docker" {
		t.Fatalf("%+v %v", sb, err)
	}
	if _, err := mgr.Get(ctx, Actor{KeyID: other.ID}, sb.ID); err == nil {
		t.Fatal("other owner should not see the sandbox")
	}
	if _, err := mgr.Create(ctx, owner, CreateInput{Image: "alpine:3.20", Network: "egress", Allow: []string{"example.com"}}); err == nil {
		t.Fatal("expected egress to be rejected")
	}
	paused, err := mgr.Pause(ctx, owner, sb.ID)
	if err != nil || paused.Status != "paused" {
		t.Fatal(err, paused.Status)
	}
	if _, err := mgr.Exec(ctx, owner, sb.ID, ExecInput{Cmd: []string{"echo", "x"}}); err == nil {
		t.Fatal("exec on paused sandbox")
	}
	if _, err := mgr.Resume(ctx, owner, sb.ID); err != nil {
		t.Fatal(err)
	}
	res, err := mgr.Exec(ctx, owner, sb.ID, ExecInput{Cmd: []string{"echo", "hi"}})
	if err != nil || string(res.Stdout) != "hi\n" || res.ExitCode != 0 {
		t.Fatalf("%q %v", res.Stdout, err)
	}
}

func TestNetworkModeValidation(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	actor := coreActor(t, st)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "allow"}); err == nil {
		t.Fatal("allow without a list")
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{"*.example.com"}}); err == nil {
		t.Fatal("wildcard")
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "bogus"}); err == nil {
		t.Fatal("bogus mode")
	}
	ok, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{"example.com"}})
	if err != nil || ok.Network != "allow" {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "full"}); err != nil {
		t.Fatal(err)
	}
}

func TestFilesTemplatesDeleteAndReaper(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	cfg := config.Default()
	cfg.Limits.ReaperIntervalSeconds = 1
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	if err := mgr.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Template: "base", TTLSeconds: intPtr(120)})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := mgr.List(ctx, actor, ListInput{Status: "running", Backend: "docker"})
	if err != nil || len(listed) != 1 || listed[0].ID != sb.ID {
		t.Fatalf("%+v %v", listed, err)
	}
	if _, err := mgr.List(ctx, Actor{KeyID: "missing", Admin: true}, ListInput{}); err != nil {
		t.Fatal(err)
	}
	updated, err := mgr.SetTTL(ctx, actor, sb.ID, 600)
	if err != nil || updated.ExpiresAt.Sub(updated.CreatedAt) < 9*time.Minute {
		t.Fatalf("%+v %v", updated, err)
	}
	if err := mgr.WriteFile(ctx, actor, sb.ID, "/workspace/note.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := mgr.ReadFile(ctx, actor, sb.ID, "/workspace/note.txt")
	if err != nil || string(body) != "hello" {
		t.Fatalf("%q %v", body, err)
	}
	entries, err := mgr.ListFiles(ctx, actor, sb.ID, "/workspace")
	if err != nil || len(entries) != 1 || entries[0].Name != "note.txt" {
		t.Fatalf("%+v %v", entries, err)
	}
	if err := mgr.WriteArchive(ctx, actor, sb.ID, "/workspace", strings.NewReader("not-a-real-tar")); err != nil {
		t.Fatal(err)
	}
	archive, err := mgr.ReadArchive(ctx, actor, sb.ID, "/workspace")
	if err != nil || string(archive) != "ok\n" {
		t.Fatalf("%q %v", archive, err)
	}
	fd.TruncateExec = true
	if _, err := mgr.ReadArchive(ctx, actor, sb.ID, "/workspace"); err == nil {
		t.Fatal("truncated archive returned as success")
	}
	fd.TruncateExec = false
	var streamed []byte
	res, err := mgr.ExecStream(ctx, actor, sb.ID, ExecInput{Cmd: []string{"echo", "stream"}}, func(chunk driver.ExecChunk) error {
		streamed = append(streamed, chunk.Data...)
		return nil
	})
	if err != nil || res.ExitCode != 0 || string(streamed) != "stream\n" {
		t.Fatalf("%q %+v %v", streamed, res, err)
	}
	if err := mgr.RemoveFile(ctx, actor, sb.ID, "/workspace/note.txt"); err != nil {
		t.Fatal(err)
	}
	tpl, err := mgr.CreateTemplate(ctx, TemplateInput{Name: "tiny", Image: "alpine:3.20", Description: "small"})
	if err != nil || tpl.Name != "tiny" {
		t.Fatal(err)
	}
	templates, err := mgr.ListTemplates(ctx)
	if err != nil || len(templates) < 2 {
		t.Fatalf("%d %v", len(templates), err)
	}
	if _, err := mgr.CreateTemplate(ctx, TemplateInput{Name: "tiny", Image: "alpine:3.20"}); err == nil {
		t.Fatal("expected duplicate template")
	}
	if err := mgr.DeleteTemplate(ctx, "base"); err == nil {
		t.Fatal("expected built-in delete to fail")
	}
	if err := mgr.DeleteTemplate(ctx, "tiny"); err != nil {
		t.Fatal(err)
	}
	stopped, err := mgr.Delete(ctx, actor, sb.ID)
	if err != nil || stopped.Status != "stopped" {
		t.Fatalf("%+v %v", stopped, err)
	}
	again, err := mgr.Delete(ctx, actor, sb.ID)
	if err != nil || again.Status != "stopped" {
		t.Fatalf("idempotent delete %+v %v", again, err)
	}
	reapCtx, cancel := context.WithCancel(ctx)
	cancel()
	mgr.RunReaper(reapCtx)
	if drv, ok := mgr.Driver("docker"); mgr.Store() == nil || mgr.Config().Backend == "" || !ok || drv == nil {
		t.Fatal("accessors")
	}
}

func asOp(err error, dst **OpError) bool {
	op, ok := err.(*OpError)
	if ok {
		*dst = op
	}
	return ok
}

func intPtr(n int) *int { return &n }

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sbx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func coreActor(t *testing.T, st *store.Store) Actor {
	t.Helper()
	_, hash, prefix := id.Key()
	key := store.APIKey{ID: id.RecordID(), Name: "owner", Hash: hash, Prefix: prefix, Scopes: []string{"sandbox"}, CreatedAt: time.Now().UTC()}
	if err := st.CreateKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return Actor{KeyID: key.ID}
}
