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

func eventTypes(t *testing.T, st *store.Store, f store.EventFilter) []string {
	t.Helper()
	items, err := st.ListEvents(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, ev := range items {
		out = append(out, ev.Type)
	}
	return out
}

func lastEvent(t *testing.T, st *store.Store, sandboxID string) store.Event {
	t.Helper()
	items, err := st.ListEvents(context.Background(), store.EventFilter{SandboxID: sandboxID})
	if err != nil || len(items) == 0 {
		t.Fatalf("no events for %s: %v", sandboxID, err)
	}
	return items[len(items)-1]
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLifecycleEvents(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	cur := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	mgr.SetClock(func() time.Time { return cur })
	ctx := context.Background()
	actor := coreActor(t, st)

	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", TTLSeconds: intPtr(60), Labels: map[string]string{"job": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Pause(ctx, actor, sb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Resume(ctx, actor, sb.ID); err != nil {
		t.Fatal(err)
	}
	cur = cur.Add(61 * time.Second)
	if _, err := mgr.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got := eventTypes(t, st, store.EventFilter{SandboxID: sb.ID})
	want := []string{EventCreated, EventPaused, EventResumed, EventExpired}
	if !equalStrings(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	ev := lastEvent(t, st, sb.ID)
	if ev.Data["reason"] != "ttl" || ev.Data["status"] != "stopped" || ev.OwnerKeyID != actor.KeyID {
		t.Fatalf("expired event %+v", ev)
	}
	if labels, _ := ev.Data["labels"].(map[string]any); labels["job"] != "a" {
		t.Fatalf("labels %+v", ev.Data)
	}

	del, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Delete(ctx, actor, del.ID); err != nil {
		t.Fatal(err)
	}
	// A second delete is a no-op and must not emit again.
	if _, err := mgr.Delete(ctx, actor, del.ID); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(t, st, store.EventFilter{SandboxID: del.ID}); !equalStrings(got, []string{EventCreated, EventDeleted}) {
		t.Fatalf("delete events %v", got)
	}

	fd.CreateErr = errors.New("boom")
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"}); err == nil {
		t.Fatal("expected create error")
	}
	fd.CreateErr = nil
	all := eventTypes(t, st, store.EventFilter{})
	if all[len(all)-1] != EventFailed {
		t.Fatalf("failed create events %v", all)
	}
}

func TestLivenessMarksOOMAndLost(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)

	oom, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	fd.Crash(oom.ContainerID, true, 137)
	fd.Vanish(gone.ContainerID)

	if _, err := mgr.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := st.GetSandbox(ctx, oom.ID)
	if row.Status != "stopped" || row.Error != "lost" {
		t.Fatalf("oom row %+v", row)
	}
	ev := lastEvent(t, st, oom.ID)
	if ev.Type != EventLost || ev.Data["reason"] != "exited" || ev.Data["exit_code"] != float64(137) || ev.Data["oom_killed"] != true {
		t.Fatalf("oom event %+v", ev)
	}
	row, _ = st.GetSandbox(ctx, gone.ID)
	if row.Status != "stopped" || row.Error != "lost" {
		t.Fatalf("gone row %+v", row)
	}
	if ev := lastEvent(t, st, gone.ID); ev.Type != EventLost || ev.Data["reason"] != "missing" {
		t.Fatalf("gone event %+v", ev)
	}
	if row, _ := st.GetSandbox(ctx, ok.ID); row.Status != "running" {
		t.Fatalf("healthy sandbox %+v", row)
	}
	// The dead container is cleaned up in the same sweep.
	if _, err := fd.Inspect(ctx, oom.ContainerID); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("dead container kept: %v", err)
	}
	// A later sweep does not report the loss twice.
	if _, err := mgr.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(t, st, store.EventFilter{SandboxID: oom.ID}); !equalStrings(got, []string{EventCreated, EventLost}) {
		t.Fatalf("oom events %v", got)
	}
}

func TestLivenessSkipsBackendThatCannotList(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	fd.Vanish(sb.ContainerID)
	fd.ListErr = errors.New("daemon down")
	mgr.checkLiveness(ctx)
	if row, _ := st.GetSandbox(ctx, sb.ID); row.Status != "running" {
		t.Fatalf("outage marked sandbox %+v", row)
	}
}

func TestDriverEvents(t *testing.T) {
	st := openStore(t)
	dock := fake.New("docker")
	gvis := fake.New("gvisor")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": dock, "gvisor": gvis}, discard())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	mgr.WatchDrivers(ctx)

	// Another Docker-backed driver sees the same container; it must not act.
	gvis.Push(driver.Event{Kind: driver.EventOOM, ID: sb.ContainerID, SandboxID: sb.ID})
	dock.Push(driver.Event{Kind: driver.EventOOM, ID: sb.ContainerID, SandboxID: sb.ID})
	waitFor(t, func() bool {
		return equalStrings(eventTypes(t, st, store.EventFilter{SandboxID: sb.ID}), []string{EventCreated, EventOOM})
	})
	if ev := lastEvent(t, st, sb.ID); ev.Data["status"] != "running" {
		t.Fatalf("oom event %+v", ev)
	}

	dock.Crash(sb.ContainerID, true, 137)
	dock.Push(driver.Event{Kind: driver.EventDie, ID: sb.ContainerID, SandboxID: sb.ID, ExitCode: 137})
	// The row is updated before the event is written, so wait for the event.
	waitFor(t, func() bool {
		return lastEvent(t, st, sb.ID).Type == EventLost
	})
	if row, _ := st.GetSandbox(ctx, sb.ID); row.Status != "stopped" {
		t.Fatalf("row %+v", row)
	}
	if ev := lastEvent(t, st, sb.ID); ev.Type != EventLost || ev.Data["reason"] != "exited" || ev.Data["oom_killed"] != true {
		t.Fatalf("lost event %+v", ev)
	}
}

func TestEventStreamReplayAndLive(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	owner := coreActor(t, st)
	other := coreActor(t, st)

	first, err := mgr.Create(ctx, owner, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, other, CreateInput{Image: "alpine:3.20"}); err != nil {
		t.Fatal(err)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	got := make(chan store.Event, 8)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- mgr.Events(streamCtx, owner, EventsInput{After: 0, Ready: func(int64) { close(ready) }}, func(ev store.Event) error {
			got <- ev
			return nil
		})
	}()
	<-ready
	next := func() store.Event {
		select {
		case ev := <-got:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
			return store.Event{}
		}
	}
	if ev := next(); ev.Type != EventCreated || ev.SandboxID != first.ID {
		t.Fatalf("replay %+v", ev)
	}
	if _, err := mgr.Delete(ctx, owner, first.ID); err != nil {
		t.Fatal(err)
	}
	if ev := next(); ev.Type != EventDeleted || ev.SandboxID != first.ID {
		t.Fatalf("live %+v", ev)
	}
	select {
	case ev := <-got:
		t.Fatalf("saw another key's event %+v", ev)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	// A negative cursor starts at the newest event.
	streamCtx, cancel = context.WithCancel(ctx)
	defer cancel()
	ready = make(chan struct{})
	go func() {
		done <- mgr.Events(streamCtx, Actor{Admin: true, KeyID: "admin"}, EventsInput{After: -1, Ready: func(int64) { close(ready) }}, func(ev store.Event) error {
			got <- ev
			return nil
		})
	}()
	<-ready
	sb, err := mgr.Create(ctx, other, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if ev := next(); ev.Type != EventCreated || ev.SandboxID != sb.ID {
		t.Fatalf("from now %+v", ev)
	}
}

func TestEventStreamCap(t *testing.T) {
	st := openStore(t)
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	actor := coreActor(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < maxEventStreams; i++ {
		ready := make(chan struct{})
		go func() {
			_ = mgr.Events(ctx, actor, EventsInput{After: -1, Ready: func(int64) { close(ready) }}, func(store.Event) error { return nil })
		}()
		<-ready
	}
	err := mgr.Events(ctx, actor, EventsInput{After: -1}, func(store.Event) error { return nil })
	var op *OpError
	if !asOp(err, &op) || op.Status != 429 {
		t.Fatalf("expected 429, got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
