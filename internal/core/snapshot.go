package core

import (
	"context"
	"errors"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// SnapshotInput commits a running sandbox. Template, when set, also saves
// the image as a template that copies the sandbox's resources and env.
type SnapshotInput struct {
	Template    string
	Description string
}

// SnapshotResult is a committed image. Image is what sandboxes start from,
// in the same sbx-template repository as built images.
type SnapshotResult struct {
	ID        string
	Image     string
	SandboxID string
	Base      string
	Workspace bool
	Template  *store.Template
	Duration  time.Duration
}

// Snapshot commits a running sandbox to an image so a prepared environment
// can be forked. /workspace is part of the image unless a volume is mounted
// there. Secrets are not.
func (m *Manager) Snapshot(ctx context.Context, actor Actor, sandboxID string, in SnapshotInput) (SnapshotResult, error) {
	if err := m.canBuild(actor); err != nil {
		return SnapshotResult{}, err
	}
	if in.Template != "" {
		if !actor.Admin {
			return SnapshotResult{}, forbidden("saving a snapshot as a template needs an admin key")
		}
		if err := validTemplateName(in.Template); err != nil {
			return SnapshotResult{}, err
		}
		if _, err := m.store.GetTemplate(ctx, in.Template); err == nil {
			return SnapshotResult{}, conflict("template already exists")
		} else if !errors.Is(err, store.ErrNotFound) {
			return SnapshotResult{}, err
		}
	}
	sb, d, err := m.running(ctx, actor, sandboxID)
	if err != nil {
		return SnapshotResult{}, err
	}
	snap, ok := d.(driver.Snapshotter)
	if !ok || !d.Capabilities().Snapshot {
		return SnapshotResult{}, unsupported(sb.Backend + " does not support snapshots")
	}
	workspace := true
	for _, mt := range sb.Mounts {
		if mt.Path == "/workspace" {
			workspace = false
		}
	}
	snapID := id.Snapshot()
	tag := buildImageRepo + ":" + snapID
	start := m.now()
	m.log.Info("snapshotting sandbox", "id", sb.ID, "image", tag, "base", sb.Image)
	err = snap.Snapshot(ctx, sb.ContainerID, driver.SnapshotSpec{Tag: tag, Base: sb.Image, Workspace: workspace})
	if err != nil {
		return SnapshotResult{}, m.asDriver(err)
	}
	m.touch(ctx, sb)
	mu := m.buildLock(tag)
	mu.Lock()
	m.touchImage(ctx, tag)
	mu.Unlock()
	res := SnapshotResult{
		ID: snapID, Image: tag, SandboxID: sb.ID, Base: sb.Image, Workspace: workspace,
		Duration: m.now().Sub(start),
	}
	if in.Template == "" {
		return res, nil
	}
	network := "none"
	if sb.Network == "full" {
		network = "full"
	}
	t, err := m.CreateTemplate(ctx, TemplateInput{
		Name: in.Template, Image: tag, Description: in.Description,
		CPU: sb.CPU, MemoryMB: sb.MemoryMB, DiskMB: sb.DiskMB, Env: sb.Env,
		Backend: sb.Backend, Network: network,
	})
	if err != nil {
		return SnapshotResult{}, err
	}
	res.Template = &t
	return res, nil
}
