package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

const maxPoolSandboxes = 32

// Replenish creates warm sandboxes until each template's pool is full.
func (m *Manager) Replenish(ctx context.Context) {
	templates, err := m.store.ListTemplates(ctx)
	if err != nil {
		m.log.Error("list templates for warm pool", "err", err)
		return
	}
	for _, tpl := range templates {
		if tpl.Warm <= 0 {
			continue
		}
		if err := m.fillTemplate(ctx, tpl); err != nil {
			m.log.Warn("warm pool", "template", tpl.Name, "err", err)
		}
	}
}

func (m *Manager) fillTemplate(ctx context.Context, tpl store.Template) error {
	backend := tpl.Backend
	if backend == "" {
		backend = m.cfg.Backend
	}
	for {
		n, err := m.store.CountWarm(ctx, tpl.Name, backend)
		if err != nil {
			return err
		}
		if n >= tpl.Warm {
			return nil
		}
		pooling, err := m.store.CountPooling(ctx)
		if err != nil {
			return err
		}
		if pooling >= maxPoolSandboxes {
			return invalid("warm pool is full")
		}
		if err := m.createWarm(ctx, tpl, backend); err != nil {
			return err
		}
	}
}

func (m *Manager) createWarm(ctx context.Context, tpl store.Template, backend string) error {
	d, ok := m.drivers[backend]
	if !ok {
		return invalid("unknown backend " + backend)
	}
	mode, network, policy, err := m.effectiveNet(ctx, Actor{}, CreateInput{Network: tpl.Network})
	if err != nil {
		return err
	}
	cpu := pickFloat(nilIfZero(tpl.CPU), m.cfg.Limits.DefaultCPU)
	mem := pickInt(nilIfZeroInt(tpl.MemoryMB), m.cfg.Limits.DefaultMemoryMB)
	disk := pickInt(nilIfZeroInt(tpl.DiskMB), m.cfg.Limits.DefaultDiskMB)
	pids := m.cfg.Limits.DefaultPIDs
	ttl := m.cfg.Limits.MaxTTLSeconds
	if err := m.checkResources(cpu, mem, disk, pids, ttl, 0); err != nil {
		return err
	}
	now := m.now()
	sb := store.Sandbox{
		ID: id.Sandbox(), Template: tpl.Name, Image: tpl.Image, Backend: backend, Status: "creating",
		CPU: cpu, MemoryMB: mem, DiskMB: disk, PIDs: pids, Env: nonNil(tpl.Env), Network: mode,
		Allow: policy.Allow, Deny: policy.Deny, EgressKbps: policy.EgressKbps, MaxConns: policy.MaxConns,
		Labels: map[string]string{}, OwnerKeyID: store.SystemOwner, CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(ttl) * time.Second), LastActivityAt: now,
	}
	if err := m.store.InsertSandbox(ctx, sb); err != nil {
		return err
	}
	handle, err := d.Create(ctx, driver.SandboxSpec{
		ID: sb.ID, Image: sb.Image, CPU: cpu, MemoryMB: mem, DiskMB: disk, PIDs: pids,
		Env: sb.Env, Network: network, Policy: policy, Labels: sb.Labels, Workdir: "/workspace",
	})
	if err != nil {
		_ = m.failWarm(ctx, sb, err.Error())
		return m.asDriver(err)
	}
	sb.ContainerID = handle.ID
	if err := m.runSetup(ctx, d, sb.ContainerID, tpl); err != nil {
		_ = m.failWarm(ctx, sb, err.Error())
		return err
	}
	sb.Status = "warm"
	if err := m.store.UpdateSandboxFrom(ctx, sb, "creating"); err != nil {
		_ = d.Delete(context.Background(), handle.ID)
		return err
	}
	m.log.Info("warm sandbox ready", "id", sb.ID, "template", tpl.Name)
	return nil
}

func (m *Manager) failWarm(ctx context.Context, sb store.Sandbox, reason string) error {
	if sb.ContainerID != "" {
		if d, ok := m.drivers[sb.Backend]; ok {
			_ = d.Delete(context.Background(), sb.ContainerID)
		}
	}
	sb.Status = "error"
	sb.Error = reason
	finished := m.now()
	sb.FinishedAt = &finished
	return m.store.UpdateSandbox(ctx, sb)
}

func (m *Manager) runSetup(ctx context.Context, d driver.Driver, containerID string, tpl store.Template) error {
	if len(tpl.Setup) == 0 {
		return nil
	}
	timeout := tpl.SetupTimeout
	if timeout <= 0 {
		timeout = m.cfg.Limits.DefaultExecSeconds
	}
	res, err := d.Exec(ctx, containerID, driver.ExecRequest{
		Cmd: tpl.Setup, Cwd: "/workspace", TimeoutSeconds: timeout,
	})
	if err != nil {
		return m.asDriver(err)
	}
	if res.ExitCode != 0 {
		msg := string(res.Stderr)
		if msg == "" {
			msg = string(res.Stdout)
		}
		return invalid(fmt.Sprintf("template setup exited %d: %s", res.ExitCode, msg))
	}
	return nil
}

func (m *Manager) drainPool(ctx context.Context, template string) {
	items, err := m.store.ListPool(ctx, template)
	if err != nil {
		m.log.Error("list warm pool", "template", template, "err", err)
		return
	}
	for _, sb := range items {
		if err := m.destroy(ctx, sb, "template deleted"); err != nil {
			m.log.Error("drain warm sandbox", "id", sb.ID, "err", err)
		}
	}
}

func (m *Manager) tryClaim(ctx context.Context, actor Actor, in CreateInput, tpl store.Template, backend string, cpu float64, mem, disk, pids, ttl, idle int) (store.Sandbox, bool, error) {
	if tpl.Name == "" || !warmCompatible(in, tpl, backend, cpu, mem, disk, pids) {
		return store.Sandbox{}, false, nil
	}
	sb, err := m.store.ClaimWarm(ctx, tpl.Name, backend, actor.KeyID, nonNil(in.Labels), ttl, idle, m.now())
	if errors.Is(err, store.ErrNotFound) {
		return store.Sandbox{}, false, nil
	}
	if err != nil {
		return store.Sandbox{}, false, err
	}
	m.Counts.Creates.Add(1)
	m.log.Info("claimed warm sandbox", "id", sb.ID, "template", tpl.Name)
	go m.Replenish(context.Background())
	return sb, true, nil
}

func warmCompatible(in CreateInput, tpl store.Template, backend string, baseCPU float64, baseMem, baseDisk, basePIDs int) bool {
	if in.Image != "" && in.Image != tpl.Image {
		return false
	}
	if in.CPU != nil && *in.CPU != baseCPU {
		return false
	}
	if in.MemoryMB != nil && *in.MemoryMB != baseMem {
		return false
	}
	if in.DiskMB != nil && *in.DiskMB != baseDisk {
		return false
	}
	if in.PIDs != nil && *in.PIDs != basePIDs {
		return false
	}
	if in.Backend != "" && in.Backend != backend {
		return false
	}
	wantNet := tpl.Network
	if wantNet == "" {
		wantNet = "none"
	}
	if in.Network != "" && in.Network != wantNet {
		return false
	}
	if len(in.Allow) > 0 || in.HasDeny || in.EgressKbps != nil || in.MaxConns != nil || len(in.Mounts) > 0 || len(in.Secrets) > 0 {
		return false
	}
	for k, v := range in.Env {
		if tpl.Env[k] != v {
			return false
		}
	}
	return true
}
