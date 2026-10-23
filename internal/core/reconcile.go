package core

import (
	"context"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// Ready reports whether startup reconciliation has finished.
func (m *Manager) Ready() bool { return m.ready.Load() }

// Reconcile adopts containers that survived a restart and marks rows whose
// containers are gone. A driver that cannot be listed is left untouched so a
// Docker outage does not mark every sandbox lost. Orphan containers are removed.
func (m *Manager) Reconcile(ctx context.Context) error {
	defer m.ready.Store(true)
	type found struct {
		backend string
		d       driver.Driver
		st      driver.Status
	}
	bySandbox := map[string]found{}
	listed := map[string]bool{}
	var first error
	for name, d := range m.drivers {
		list, err := d.List(ctx)
		if err != nil {
			m.log.Warn("reconcile list failed", "backend", name, "err", err)
			if first == nil {
				first = err
			}
			continue
		}
		listed[name] = true
		for _, st := range list {
			if st.SandboxID == "" {
				if err := d.Delete(ctx, st.ID); err != nil && first == nil {
					first = err
				}
				continue
			}
			bySandbox[st.SandboxID] = found{backend: name, d: d, st: st}
		}
	}
	rows, err := m.store.ListSandboxes(ctx, store.SandboxFilter{})
	if err != nil {
		return err
	}
	for _, sb := range rows {
		if sb.Status != "creating" && sb.Status != "running" && sb.Status != "paused" {
			continue
		}
		if !listed[sb.Backend] {
			continue
		}
		hit, ok := bySandbox[sb.ID]
		if !ok || hit.st.State == driver.StateStopped || hit.st.State == driver.StateUnknown {
			reason := "missing"
			if ok {
				reason = "exited"
			}
			m.markLost(ctx, sb, reason)
			continue
		}
		next := "running"
		if hit.st.State == driver.StatePaused {
			next = "paused"
		}
		if sb.Status != next || sb.ContainerID != hit.st.ID {
			sb.Status = next
			sb.ContainerID = hit.st.ID
			sb.Error = ""
			if err := m.store.UpdateSandbox(ctx, sb); err != nil && first == nil {
				first = err
			}
			m.log.Info("sandbox adopted", "id", sb.ID, "status", next)
		}
		delete(bySandbox, sb.ID)
	}
	if err := m.LoadPreviews(ctx); err != nil {
		m.log.Warn("preview restore failed", "err", err)
		if first == nil {
			first = err
		}
	}
	for id, hit := range bySandbox {
		sb, err := m.store.GetSandbox(ctx, id)
		if err == nil && (sb.Status == "creating" || sb.Status == "running" || sb.Status == "paused") {
			continue
		}
		if err := hit.d.Delete(ctx, hit.st.ID); err != nil {
			m.log.Warn("orphan delete failed", "id", hit.st.ID, "err", err)
		}
	}
	return first
}

func (m *Manager) markLost(ctx context.Context, sb store.Sandbox, reason string) {
	sb.Status = "stopped"
	sb.Error = "lost"
	finished := m.now()
	sb.FinishedAt = &finished
	if err := m.store.UpdateSandbox(ctx, sb); err != nil {
		m.log.Warn("mark lost failed", "id", sb.ID, "err", err)
		return
	}
	m.log.Info("sandbox lost", "id", sb.ID)
	m.emit(ctx, EventLost, sb, map[string]any{"reason": reason})
}
