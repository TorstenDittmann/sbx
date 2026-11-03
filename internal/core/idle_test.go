package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

type idleHarness struct {
	mgr   *Manager
	st    *store.Store
	fd    *fake.Driver
	actor Actor
	cur   time.Time
}

func newIdleHarness(t *testing.T) *idleHarness {
	t.Helper()
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	gv := fake.New("gvisor")
	h := &idleHarness{st: st, fd: fd, cur: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	h.mgr = New(cfg, st, map[string]driver.Driver{"docker": fd, "gvisor": gv}, discard())
	h.mgr.SetClock(func() time.Time { return h.cur })
	h.actor = coreActor(t, st)
	return h
}

func (h *idleHarness) get(t *testing.T, id string) store.Sandbox {
	t.Helper()
	sb, err := h.st.GetSandbox(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func (h *idleHarness) sweep(t *testing.T) int {
	t.Helper()
	n, err := h.mgr.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIdleActionValidation(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	cases := []struct {
		in     CreateInput
		status int
	}{
		{CreateInput{Image: "alpine:3.20", IdleAction: "sleep", IdleTimeoutSeconds: intPtr(30)}, 400},
		{CreateInput{Image: "alpine:3.20", IdleAction: IdleHibernate}, 400},
		{CreateInput{Image: "alpine:3.20", IdleAction: IdleHibernate, IdleTimeoutSeconds: intPtr(30), Backend: "gvisor"}, 400},
	}
	for _, c := range cases {
		_, err := h.mgr.Create(ctx, h.actor, c.in)
		var op *OpError
		if !errors.As(err, &op) || op.Status != c.status {
			t.Fatalf("%+v: got %v, want %d", c.in, err, c.status)
		}
	}
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{Image: "alpine:3.20"})
	if err != nil || sb.IdleAction != IdleStop {
		t.Fatalf("default idle action %q %v", sb.IdleAction, err)
	}
}

func TestIdlePauseAndAutoResume(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(3600), IdleTimeoutSeconds: intPtr(30), IdleAction: IdlePause,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.cur = h.cur.Add(31 * time.Second)
	if n := h.sweep(t); n != 0 {
		t.Fatalf("pause is not a stop, reaped %d", n)
	}
	if got := h.get(t, sb.ID); got.Status != "paused" {
		t.Fatalf("status %s", got.Status)
	}
	if st, _ := h.fd.Inspect(ctx, sb.ContainerID); st.State != driver.StatePaused {
		t.Fatalf("container %s", st.State)
	}
	if ev := lastEvent(t, h.st, sb.ID); ev.Type != EventPaused || ev.Data["reason"] != "idle" {
		t.Fatalf("event %+v", ev)
	}
	// Paused, only the TTL applies: later sweeps leave it alone.
	h.cur = h.cur.Add(10 * time.Minute)
	h.sweep(t)
	if got := h.get(t, sb.ID); got.Status != "paused" {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := h.mgr.Exec(ctx, h.actor, sb.ID, ExecInput{Cmd: []string{"true"}}); err != nil {
		t.Fatalf("exec should resume: %v", err)
	}
	if got := h.get(t, sb.ID); got.Status != "running" {
		t.Fatalf("status %s", got.Status)
	}
	if ev := lastEvent(t, h.st, sb.ID); ev.Type != EventResumed || ev.Data["auto"] != true {
		t.Fatalf("event %+v", ev)
	}
}

func TestManualPauseWithStopActionStillRefusesExec(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.Pause(ctx, h.actor, sb.ID); err != nil {
		t.Fatal(err)
	}
	_, err = h.mgr.Exec(ctx, h.actor, sb.ID, ExecInput{Cmd: []string{"true"}})
	var op *OpError
	if !errors.As(err, &op) || op.Status != 409 {
		t.Fatalf("got %v", err)
	}
}

func TestHibernateAndWakeFromSnapshot(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(3600), IdleTimeoutSeconds: intPtr(30), IdleAction: IdleHibernate,
		Secrets: map[string]string{"TOKEN": "s3cret"}, Labels: map[string]string{"app": "web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.WriteFile(ctx, h.actor, sb.ID, "/workspace/notes.txt", []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.cur = h.cur.Add(31 * time.Second)
	if n := h.sweep(t); n != 0 {
		t.Fatalf("hibernate is not a stop, reaped %d", n)
	}
	got := h.get(t, sb.ID)
	if got.Status != "hibernated" || got.ContainerID != "" || got.SnapshotImage == "" {
		t.Fatalf("after idle %+v", got)
	}
	first := got.SnapshotImage
	if list, _ := h.fd.List(ctx); len(list) != 0 {
		t.Fatalf("container should be gone, have %+v", list)
	}
	if ev := lastEvent(t, h.st, sb.ID); ev.Type != EventHibernated {
		t.Fatalf("event %+v", ev)
	}
	// A hibernated sandbox still counts toward its key, not the node.
	if n, _ := h.st.CountActive(ctx, h.actor.KeyID); n != 1 {
		t.Fatalf("key count %d", n)
	}
	if n, _ := h.st.CountAllActive(ctx); n != 0 {
		t.Fatalf("node count %d", n)
	}
	// Image collection must not take the snapshot of a hibernated sandbox.
	keep, err := h.mgr.referencedImages(ctx)
	if err != nil || !keep[first] {
		t.Fatalf("snapshot not referenced: %v", err)
	}

	data, err := h.mgr.ReadFile(ctx, h.actor, sb.ID, "/workspace/notes.txt")
	if err != nil || string(data) != "kept" {
		t.Fatalf("read after wake %q %v", data, err)
	}
	got = h.get(t, sb.ID)
	if got.Status != "running" || got.ContainerID == "" {
		t.Fatalf("after wake %+v", got)
	}
	spec, ok := h.fd.Spec(got.ContainerID)
	if !ok || spec.Image != first || spec.Env["TOKEN"] != "s3cret" || spec.Labels["app"] != "web" || spec.Workdir != "/workspace" {
		t.Fatalf("woken spec %+v", spec)
	}
	if ev := lastEvent(t, h.st, sb.ID); ev.Type != EventResumed || ev.Data["from"] != "snapshot" {
		t.Fatalf("event %+v", ev)
	}

	// Going idle again replaces the snapshot, and the new one is based on
	// the image the container ran from.
	h.cur = h.cur.Add(31 * time.Second)
	h.sweep(t)
	got = h.get(t, sb.ID)
	if got.Status != "hibernated" || got.SnapshotImage == first {
		t.Fatalf("second hibernate %+v", got)
	}
	snaps := h.fd.Snapshots()
	if last := snaps[len(snaps)-1]; last.Base != first {
		t.Fatalf("second snapshot base %s, want %s", last.Base, first)
	}
	if has(t, h.fd, first) {
		t.Fatal("the old snapshot should be removed")
	}

	// Resume wakes explicitly too.
	woken, err := h.mgr.Resume(ctx, h.actor, sb.ID)
	if err != nil || woken.Status != "running" {
		t.Fatalf("resume %+v %v", woken, err)
	}
	if _, err := h.mgr.Exec(ctx, h.actor, sb.ID, ExecInput{Cmd: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
}

func TestHibernatedSandboxExpiresAndDropsSnapshot(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(120), IdleTimeoutSeconds: intPtr(30), IdleAction: IdleHibernate,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.cur = h.cur.Add(31 * time.Second)
	h.sweep(t)
	image := h.get(t, sb.ID).SnapshotImage
	if image == "" {
		t.Fatal("not hibernated")
	}
	if _, err := h.mgr.SetTTL(ctx, h.actor, sb.ID, 60); err != nil {
		t.Fatalf("ttl on hibernated: %v", err)
	}
	h.cur = h.cur.Add(61 * time.Second)
	if n := h.sweep(t); n != 1 {
		t.Fatalf("reaped %d", n)
	}
	got := h.get(t, sb.ID)
	if got.Status != "stopped" || got.Error != "ttl" {
		t.Fatalf("after ttl %+v", got)
	}
	if has(t, h.fd, image) {
		t.Fatal("snapshot should be removed with the sandbox")
	}
	if _, err := h.mgr.Exec(ctx, h.actor, sb.ID, ExecInput{Cmd: []string{"true"}}); err == nil {
		t.Fatal("a stopped sandbox must not wake")
	}
}

func TestHibernateFailureFallsBackToPause(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(3600), IdleTimeoutSeconds: intPtr(30), IdleAction: IdleHibernate,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.fd.SnapshotErr = errors.New("disk full")
	h.cur = h.cur.Add(31 * time.Second)
	h.sweep(t)
	if got := h.get(t, sb.ID); got.Status != "paused" || got.SnapshotImage != "" {
		t.Fatalf("fallback %+v", got)
	}
	if _, err := h.mgr.Exec(ctx, h.actor, sb.ID, ExecInput{Cmd: []string{"true"}}); err != nil {
		t.Fatalf("exec should resume: %v", err)
	}
}

func TestHibernateSkipsSandboxUsedDuringSnapshot(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(3600), IdleTimeoutSeconds: intPtr(30), IdleAction: IdleHibernate,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := h.get(t, sb.ID)
	h.cur = h.cur.Add(31 * time.Second)
	if err := h.st.TouchSandbox(ctx, sb.ID, h.cur); err != nil {
		t.Fatal(err)
	}
	if err := h.st.HibernateSandbox(ctx, sb.ID, "sbx-template:snap", stale.LastActivityAt); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hibernate after activity: %v", err)
	}
	if got := h.get(t, sb.ID); got.Status != "running" {
		t.Fatalf("status %s", got.Status)
	}
}

func TestConcurrentRequestsWakeOnce(t *testing.T) {
	h := newIdleHarness(t)
	ctx := context.Background()
	sb, err := h.mgr.Create(ctx, h.actor, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(3600), IdleTimeoutSeconds: intPtr(30), IdleAction: IdleHibernate,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.cur = h.cur.Add(31 * time.Second)
	h.sweep(t)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := h.mgr.Exec(ctx, h.actor, sb.ID, ExecInput{Cmd: []string{"true"}})
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, typ := range eventTypes(t, h.st, store.EventFilter{SandboxID: sb.ID}) {
		if typ == EventResumed {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("woken %d times", n)
	}
}
