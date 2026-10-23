package core

import (
	"context"
	"errors"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// Lifecycle event types. Every event carries the sandbox status after the
// change, so a client can keep its view without a follow-up GET.
const (
	EventCreated = "sandbox.created"
	EventFailed  = "sandbox.failed"
	EventDeleted = "sandbox.deleted"
	EventExpired = "sandbox.expired"
	EventPaused  = "sandbox.paused"
	EventResumed = "sandbox.resumed"
	// EventOOM is a process in the sandbox killed for memory. The sandbox
	// usually keeps running; if it does not, sandbox.lost follows.
	EventOOM = "sandbox.oom"
	// EventLost is a sandbox that stopped without sbx stopping it.
	EventLost = "sandbox.lost"
)

// maxEventStreams caps concurrent event streams per API key.
const maxEventStreams = 16

// EventsInput selects what an event stream sends. After is the last Seq the
// client saw; a negative After starts at the newest event, so only events
// from now on are sent. Ready, when set, runs once the stream is accepted,
// before any event, with the cursor the stream starts after. Ping, when set, runs after PingEvery without an event.
type EventsInput struct {
	After     int64
	SandboxID string
	Ready     func(cursor int64)
	Ping      func() error
	PingEvery time.Duration
}

// emit records a lifecycle event and wakes every stream. Warm pool sandboxes
// have no owner a client could know about, so their events are not kept.
// Failures are logged: the state change already happened and must not fail.
func (m *Manager) emit(ctx context.Context, typ string, sb store.Sandbox, data map[string]any) {
	if sb.OwnerKeyID == store.SystemOwner || sb.OwnerKeyID == "" {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["status"] = sb.Status
	data["backend"] = sb.Backend
	if sb.Template != "" {
		data["template"] = sb.Template
	}
	if len(sb.Labels) > 0 {
		data["labels"] = sb.Labels
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := m.store.InsertEvent(ctx, store.Event{
		Type: typ, SandboxID: sb.ID, OwnerKeyID: sb.OwnerKeyID, Time: m.now(), Data: data,
	}); err != nil {
		m.log.Warn("event insert failed", "type", typ, "id", sb.ID, "err", err)
		return
	}
	m.evMu.Lock()
	close(m.evWake)
	m.evWake = make(chan struct{})
	m.evMu.Unlock()
}

func (m *Manager) eventWake() <-chan struct{} {
	m.evMu.Lock()
	defer m.evMu.Unlock()
	return m.evWake
}

// stopEvent maps a destroy reason to the event it produces.
func stopEvent(reason string) (string, map[string]any) {
	switch reason {
	case "":
		return EventDeleted, nil
	case "ttl", "idle":
		return EventExpired, map[string]any{"reason": reason}
	default:
		return EventFailed, map[string]any{"reason": reason}
	}
}

// Events sends lifecycle events to fn in Seq order until ctx ends or fn
// fails. The store is the only buffer: a stream reads rows after its cursor
// whenever a new event is written, so replay and live delivery are one path
// and a slow client never drops events. A sandbox key sees its own sandboxes.
func (m *Manager) Events(ctx context.Context, actor Actor, in EventsInput, fn func(store.Event) error) error {
	key := "events:" + actor.KeyID
	m.evMu.Lock()
	if m.evStreams[key] >= maxEventStreams {
		m.evMu.Unlock()
		return quota("too many event streams for this key")
	}
	m.evStreams[key]++
	m.evMu.Unlock()
	defer func() {
		m.evMu.Lock()
		m.evStreams[key]--
		if m.evStreams[key] <= 0 {
			delete(m.evStreams, key)
		}
		m.evMu.Unlock()
	}()

	cursor := in.After
	if cursor < 0 {
		last, err := m.store.LastEventSeq(ctx)
		if err != nil {
			return err
		}
		cursor = last
	}
	f := store.EventFilter{SandboxID: in.SandboxID, Limit: 200}
	if !actor.Admin {
		f.OwnerID = actor.KeyID
	}
	if in.Ready != nil {
		in.Ready(cursor)
	}
	var ping <-chan time.Time
	if in.Ping != nil && in.PingEvery > 0 {
		t := time.NewTicker(in.PingEvery)
		defer t.Stop()
		ping = t.C
	}
	for {
		// Take the wake channel before reading, so an event written between
		// the read and the wait still wakes this stream.
		wake := m.eventWake()
		f.After = cursor
		items, err := m.store.ListEvents(ctx, f)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		for _, ev := range items {
			if err := fn(ev); err != nil {
				return err
			}
			cursor = ev.Seq
		}
		if len(items) == f.Limit {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-ping:
			if err := in.Ping(); err != nil {
				return err
			}
		}
	}
}

// checkLiveness stops running and paused sandboxes whose container exited
// or vanished without sbx stopping it. Rows are read before containers are
// listed: a row that is running at that point already has its container, so
// a create in flight is never taken for a loss. A backend that cannot be
// listed is skipped, so a Docker outage does not mark every sandbox lost.
func (m *Manager) checkLiveness(ctx context.Context) {
	byBackend := map[string][]store.Sandbox{}
	for _, status := range []string{"running", "paused"} {
		rows, err := m.store.ListSandboxes(ctx, store.SandboxFilter{Status: status})
		if err != nil {
			m.log.Warn("liveness check failed", "err", err)
			return
		}
		for _, sb := range rows {
			if sb.ContainerID != "" {
				byBackend[sb.Backend] = append(byBackend[sb.Backend], sb)
			}
		}
	}
	for name, live := range byBackend {
		d, ok := m.drivers[name]
		if !ok {
			continue
		}
		list, err := d.List(ctx)
		if err != nil {
			continue
		}
		found := map[string]driver.Status{}
		alive := map[string]bool{}
		for _, st := range list {
			found[st.ID] = st
			if st.State != driver.StateStopped && st.SandboxID != "" {
				alive[st.SandboxID] = true
			}
		}
		for _, sb := range live {
			st, ok := found[sb.ContainerID]
			// A live container under another ID is Reconcile's to adopt.
			if (ok && st.State != driver.StateStopped) || alive[sb.ID] {
				continue
			}
			m.sandboxDied(ctx, sb, d, ok)
		}
	}
}

// sandboxDied marks sb stopped after its container exited (exists) or
// disappeared, and emits sandbox.lost. The update only applies while the
// row is still running or paused, so a concurrent delete wins.
func (m *Manager) sandboxDied(ctx context.Context, sb store.Sandbox, d driver.Driver, exists bool) {
	data := map[string]any{"reason": "missing"}
	if exists {
		data["reason"] = "exited"
		st, err := d.Inspect(ctx, sb.ContainerID)
		switch {
		case errors.Is(err, driver.ErrNotFound):
			data["reason"] = "missing"
		case err != nil:
		case st.State != driver.StateStopped:
			// It came back between the list and the inspect.
			return
		default:
			data["exit_code"] = st.ExitCode
			// Docker keeps OOMKilled once any process in the sandbox was
			// OOM-killed, so it is a hint, not the cause of this exit.
			data["oom_killed"] = st.OOMKilled
		}
	}
	m.mu.Lock()
	cur := sb
	cur.Status = "stopped"
	cur.Error = "lost"
	finished := m.now()
	cur.FinishedAt = &finished
	if err := m.store.UpdateSandboxFrom(ctx, cur, "running", "paused"); err != nil {
		m.mu.Unlock()
		if !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("mark lost failed", "id", sb.ID, "err", err)
		}
		return
	}
	m.dropPreviews(ctx, cur.ID)
	m.mu.Unlock()
	m.log.Warn("sandbox lost", "id", sb.ID, "reason", data["reason"])
	m.emit(ctx, EventLost, cur, data)
}

// WatchDrivers follows runtime events from every backend that pushes them,
// reconnecting with backoff, until ctx ends. The reaper's liveness check
// still runs, so a missed event only delays detection to the next sweep.
func (m *Manager) WatchDrivers(ctx context.Context) {
	for name, d := range m.drivers {
		src, ok := d.(driver.EventSource)
		if !ok {
			continue
		}
		go m.watchDriver(ctx, name, d, src)
	}
}

func (m *Manager) watchDriver(ctx context.Context, name string, d driver.Driver, src driver.EventSource) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := src.WatchEvents(ctx, func(ev driver.Event) { m.onDriverEvent(ctx, name, d, ev) })
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		m.log.Debug("driver event stream ended", "backend", name, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// onDriverEvent handles one runtime event. Docker-backed drivers share a
// daemon and each sees every sbx container, so only the sandbox's own
// backend acts on it.
func (m *Manager) onDriverEvent(ctx context.Context, backend string, d driver.Driver, ev driver.Event) {
	if ev.SandboxID == "" {
		return
	}
	sb, err := m.store.GetSandbox(ctx, ev.SandboxID)
	if err != nil || sb.Backend != backend || sb.ContainerID != ev.ID {
		return
	}
	if sb.Status != "running" && sb.Status != "paused" {
		return
	}
	switch ev.Kind {
	case driver.EventOOM:
		m.emit(ctx, EventOOM, sb, nil)
	case driver.EventDie:
		m.sandboxDied(ctx, sb, d, true)
	}
}
