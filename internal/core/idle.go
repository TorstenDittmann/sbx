package core

import (
	"context"
	"errors"
	"sync"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// Idle actions: what the reaper does with a sandbox that has gone
// idle_timeout_seconds without activity.
const (
	// IdleStop stops the sandbox. It is the default.
	IdleStop = "stop"
	// IdlePause freezes the container. Memory stays allocated, CPU is freed,
	// and the next request resumes it in place.
	IdlePause = "pause"
	// IdleHibernate snapshots the sandbox, /workspace included, and removes
	// its container, so it holds no CPU or memory. The next request starts
	// a new container from the snapshot. Running processes and memory do
	// not survive.
	IdleHibernate = "hibernate"
)

// EventHibernated is a sandbox saved to a snapshot after going idle.
const EventHibernated = "sandbox.hibernated"

// checkIdleAction validates an idle action against the backend.
func checkIdleAction(action string, idle int, d driver.Driver) (string, error) {
	switch action {
	case "", IdleStop:
		return IdleStop, nil
	case IdlePause, IdleHibernate:
	default:
		return "", invalid("idle_action must be stop, pause, or hibernate")
	}
	if idle <= 0 {
		return "", invalid("idle_action " + action + " needs idle_timeout_seconds")
	}
	caps := d.Capabilities()
	if action == IdlePause && !caps.Pause {
		return "", unsupported(d.Name() + " does not support pause")
	}
	if action == IdleHibernate {
		if _, ok := d.(driver.Snapshotter); !ok || !caps.Snapshot {
			return "", unsupported(d.Name() + " does not support snapshots, so it cannot hibernate")
		}
	}
	return action, nil
}

// autoResumes reports whether a request to a paused or hibernated sandbox
// wakes it instead of failing.
func autoResumes(sb store.Sandbox) bool {
	return sb.IdleAction == IdlePause || sb.IdleAction == IdleHibernate
}

// sandboxLock serializes pause, hibernate, and wake for one sandbox. It is
// taken before m.mu, never after.
func (m *Manager) sandboxLock(sandboxID string) *sync.Mutex {
	v, _ := m.sbLocks.LoadOrStore(sandboxID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// runImage is the image the sandbox's container runs from.
func runImage(sb store.Sandbox) string {
	if sb.SnapshotImage != "" {
		return sb.SnapshotImage
	}
	return sb.Image
}

// idleAct applies the sandbox's idle action. Stop is handled by the caller.
func (m *Manager) idleAct(ctx context.Context, sb store.Sandbox) error {
	switch sb.IdleAction {
	case IdlePause:
		return m.idlePause(ctx, sb)
	case IdleHibernate:
		return m.hibernate(ctx, sb)
	}
	return m.destroy(ctx, sb, "idle")
}

// idlePause freezes an idle running sandbox.
func (m *Manager) idlePause(ctx context.Context, sb store.Sandbox) error {
	mu := m.sandboxLock(sb.ID)
	mu.Lock()
	defer mu.Unlock()
	cur, err := m.store.GetSandbox(ctx, sb.ID)
	if err != nil || cur.Status != "running" {
		return ignoreGone(err)
	}
	return m.idlePauseLocked(ctx, cur)
}

// idlePauseLocked pauses cur, read under its sandbox lock.
func (m *Manager) idlePauseLocked(ctx context.Context, cur store.Sandbox) error {
	d, err := m.backend(cur)
	if err != nil {
		return err
	}
	if !d.Capabilities().Pause {
		return m.destroy(ctx, cur, "idle")
	}
	if err := d.Pause(ctx, cur.ContainerID); err != nil {
		return err
	}
	cur.Status = "paused"
	if err := m.store.UpdateSandboxFrom(ctx, cur, "running"); err != nil {
		// Deleted meanwhile: destroy removes the container.
		return ignoreGone(err)
	}
	m.Counts.IdlePaused.Add(1)
	m.log.Info("sandbox paused", "id", cur.ID, "reason", "idle")
	m.emit(ctx, EventPaused, cur, map[string]any{"reason": "idle"})
	return nil
}

// hibernate snapshots an idle running sandbox and removes its container. If
// the snapshot fails the sandbox is paused instead, so its state is kept and
// only its TTL applies from then on.
func (m *Manager) hibernate(ctx context.Context, sb store.Sandbox) error {
	mu := m.sandboxLock(sb.ID)
	mu.Lock()
	defer mu.Unlock()
	cur, err := m.store.GetSandbox(ctx, sb.ID)
	if err != nil || cur.Status != "running" {
		return ignoreGone(err)
	}
	d, err := m.backend(cur)
	if err != nil {
		return err
	}
	snap, ok := d.(driver.Snapshotter)
	if !ok || !d.Capabilities().Snapshot {
		return m.idlePauseLocked(ctx, cur)
	}
	prev := cur.SnapshotImage
	tag, _, err := m.commit(ctx, cur, snap)
	if err != nil {
		m.log.Warn("hibernate snapshot failed, pausing instead", "id", cur.ID, "err", err)
		return m.idlePauseLocked(ctx, cur)
	}
	if err := m.store.HibernateSandbox(ctx, cur.ID, tag, cur.LastActivityAt); err != nil {
		// Used or deleted during the snapshot. It stays as it is, and
		// the next sweep looks again.
		m.removeImage(ctx, cur.Backend, tag)
		return ignoreGone(err)
	}
	if err := d.Delete(ctx, cur.ContainerID); err != nil && !errors.Is(err, driver.ErrNotFound) {
		m.log.Warn("hibernate container delete failed", "id", cur.ID, "err", err)
	}
	if prev != "" && prev != tag {
		m.removeImage(ctx, cur.Backend, prev)
	}
	cur.Status = "hibernated"
	cur.ContainerID = ""
	cur.SnapshotImage = tag
	m.Counts.Hibernated.Add(1)
	m.log.Info("sandbox hibernated", "id", cur.ID, "image", tag)
	m.emit(ctx, EventHibernated, cur, map[string]any{"reason": "idle"})
	return nil
}

// wake brings a paused or hibernated sandbox back to running. A hibernated
// sandbox gets a new container from its snapshot, with the same resources,
// env, secrets, network policy, labels, and volumes.
func (m *Manager) wake(ctx context.Context, sb store.Sandbox, auto bool) (store.Sandbox, error) {
	mu := m.sandboxLock(sb.ID)
	mu.Lock()
	defer mu.Unlock()
	cur, err := m.store.GetSandbox(ctx, sb.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Sandbox{}, notFound("sandbox not found")
	}
	if err != nil {
		return store.Sandbox{}, err
	}
	switch cur.Status {
	case "running":
		return cur, nil
	case "paused":
		return m.resumePaused(ctx, cur, auto)
	case "hibernated":
	default:
		return store.Sandbox{}, conflict("sandbox is not running")
	}
	d, err := m.backend(cur)
	if err != nil {
		return store.Sandbox{}, err
	}
	if err := m.checkNodeCap(ctx); err != nil {
		return store.Sandbox{}, err
	}
	if err := m.mountsStillExist(ctx, cur.Mounts); err != nil {
		return store.Sandbox{}, err
	}
	network := driver.NetNone
	if cur.Network != "none" {
		network = driver.NetFull
	}
	handle, err := d.Create(ctx, driver.SandboxSpec{
		ID: cur.ID, Image: cur.SnapshotImage, CPU: cur.CPU, MemoryMB: cur.MemoryMB, DiskMB: cur.DiskMB, PIDs: cur.PIDs,
		Env: mergeEnv(cur.Env, cur.Secrets), Network: network, Policy: m.storedPolicy(cur), Labels: cur.Labels,
		Workdir: "/workspace", Mounts: driverMounts(cur.Mounts),
	})
	if err != nil {
		return store.Sandbox{}, m.asDriver(err)
	}
	cur.ContainerID = handle.ID
	cur.Status = "running"
	cur.LastActivityAt = m.now()
	if err := m.store.UpdateSandboxFrom(ctx, cur, "hibernated"); err != nil {
		_ = d.Delete(context.WithoutCancel(ctx), handle.ID)
		return store.Sandbox{}, stale(err)
	}
	m.touchImage(ctx, cur.SnapshotImage)
	m.log.Info("sandbox woken", "id", cur.ID, "image", cur.SnapshotImage)
	m.emit(ctx, EventResumed, cur, map[string]any{"from": "snapshot", "auto": auto})
	return cur, nil
}

// resumePaused unpauses a paused sandbox's container.
func (m *Manager) resumePaused(ctx context.Context, sb store.Sandbox, auto bool) (store.Sandbox, error) {
	d, err := m.backend(sb)
	if err != nil {
		return store.Sandbox{}, err
	}
	if err := d.Resume(ctx, sb.ContainerID); err != nil {
		return store.Sandbox{}, m.asDriver(err)
	}
	sb.Status = "running"
	sb.LastActivityAt = m.now()
	if err := m.store.UpdateSandboxFrom(ctx, sb, "paused"); err != nil {
		return store.Sandbox{}, stale(err)
	}
	var data map[string]any
	if auto {
		data = map[string]any{"auto": true}
	}
	m.emit(ctx, EventResumed, sb, data)
	return sb, nil
}

// commit snapshots sb to a new tag in the build repository. A snapshot of a
// private base stays private. It does not record activity on the sandbox.
func (m *Manager) commit(ctx context.Context, sb store.Sandbox, snap driver.Snapshotter) (string, bool, error) {
	workspace := true
	for _, mt := range sb.Mounts {
		if mt.Path == "/workspace" {
			workspace = false
		}
	}
	tag := buildImageRepo + ":" + id.Snapshot()
	// A snapshot holds its base's layers, so a private base keeps it private:
	// a fork must still be allowed to pull the base.
	priv := sb.Image
	if driver.Repository(priv) != driver.Repository(buildImageRepo) {
		priv = driver.Repository(priv)
	}
	if _, private, err := m.store.PrivateImage(ctx, priv); err != nil {
		return "", false, err
	} else if private {
		if err := m.store.MarkPrivateImage(ctx, tag, sb.Image, m.now()); err != nil {
			return "", false, err
		}
	}
	base := runImage(sb)
	m.log.Info("snapshotting sandbox", "id", sb.ID, "image", tag, "base", base)
	if err := snap.Snapshot(ctx, sb.ContainerID, driver.SnapshotSpec{Tag: tag, Base: base, Workspace: workspace}); err != nil {
		return "", false, m.asDriver(err)
	}
	mu := m.buildLock(tag)
	mu.Lock()
	m.touchImage(ctx, tag)
	mu.Unlock()
	return tag, workspace, nil
}

// removeImage drops a hibernation snapshot nothing needs any more. Failures
// are left to image collection.
func (m *Manager) removeImage(ctx context.Context, backend, ref string) {
	c, ok := m.drivers[backend].(driver.ImageCollector)
	if !ok {
		return
	}
	ctx = context.WithoutCancel(ctx)
	mu := m.buildLock(ref)
	mu.Lock()
	defer mu.Unlock()
	if err := c.RemoveImage(ctx, ref); err != nil {
		m.log.Debug("hibernation image kept", "image", ref, "err", err)
		return
	}
	_ = m.store.ForgetImage(ctx, ref)
}

// storedPolicy rebuilds the network policy a sandbox was created with.
func (m *Manager) storedPolicy(sb store.Sandbox) driver.NetPolicy {
	return driver.NetPolicy{
		Allow: sb.Allow, Deny: sb.Deny,
		BlockPrivate: m.cfg.Network.BlockPrivate, BlockMetadata: m.cfg.Network.BlockMetadata,
		PrivateAllow: append([]string{}, m.cfg.Network.PrivateAllow...),
		DNS:          append([]string{}, m.cfg.Network.DNS...),
		EgressKbps:   sb.EgressKbps, MaxConns: sb.MaxConns,
	}
}

func ignoreGone(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}
