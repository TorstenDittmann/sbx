package core

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// MountInput attaches an existing volume when a sandbox is created.
type MountInput struct {
	VolumeID string
	Path     string
}

// CreateVolume creates a named volume for the caller's backend.
func (m *Manager) CreateVolume(ctx context.Context, actor Actor, name, backend string) (store.Volume, error) {
	if err := validVolumeName(name); err != nil {
		return store.Volume{}, err
	}
	if backend == "" {
		backend = m.cfg.Backend
	}
	d, ok := m.drivers[backend]
	if !ok {
		return store.Volume{}, invalid("unknown backend " + backend)
	}
	ops, ok := d.(driver.VolumeOps)
	if !ok {
		return store.Volume{}, unsupported("backend does not support volumes")
	}
	volID := id.Volume()
	vol := store.Volume{
		ID: volID, Name: name, OwnerKeyID: actor.KeyID, Backend: backend,
		DockerName: "sbx-" + strings.TrimPrefix(volID, "vol_"),
		CreatedAt:  m.now(),
	}
	if err := ops.CreateVolume(ctx, vol.DockerName); err != nil {
		return store.Volume{}, m.asDriver(err)
	}
	if err := m.store.InsertVolume(ctx, vol); err != nil {
		_ = ops.DeleteVolume(context.Background(), vol.DockerName)
		if strings.Contains(err.Error(), "UNIQUE") {
			return store.Volume{}, conflict("volume name already exists")
		}
		return store.Volume{}, err
	}
	return vol, nil
}

// ListVolumes returns volumes visible to the caller.
func (m *Manager) ListVolumes(ctx context.Context, actor Actor) ([]store.Volume, error) {
	owner := ""
	if !actor.Admin {
		owner = actor.KeyID
	}
	return m.store.ListVolumes(ctx, owner)
}

// GetVolume loads one volume the caller may see.
func (m *Manager) GetVolume(ctx context.Context, actor Actor, volumeID string) (store.Volume, error) {
	vol, err := m.store.GetVolume(ctx, volumeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Volume{}, notFound("volume not found")
		}
		return store.Volume{}, err
	}
	if !actor.Admin && vol.OwnerKeyID != actor.KeyID {
		return store.Volume{}, notFound("volume not found")
	}
	return vol, nil
}

// DeleteVolume removes a volume that no live sandbox mounts.
func (m *Manager) DeleteVolume(ctx context.Context, actor Actor, volumeID string) error {
	vol, err := m.GetVolume(ctx, actor, volumeID)
	if err != nil {
		return err
	}
	inUse, err := m.store.VolumeInUse(ctx, vol.ID)
	if err != nil {
		return err
	}
	if inUse {
		return conflict("volume is mounted by a sandbox")
	}
	d, ok := m.drivers[vol.Backend]
	if !ok {
		return invalid("unknown backend " + vol.Backend)
	}
	ops, ok := d.(driver.VolumeOps)
	if !ok {
		return unsupported("backend does not support volumes")
	}
	if err := ops.DeleteVolume(ctx, vol.DockerName); err != nil {
		return m.asDriver(err)
	}
	return m.store.DeleteVolume(ctx, vol.ID)
}

func (m *Manager) resolveMounts(ctx context.Context, actor Actor, backend string, in []MountInput) ([]store.Mount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > 8 {
		return nil, invalid("at most 8 mounts")
	}
	seenPath := map[string]struct{}{}
	out := make([]store.Mount, 0, len(in))
	for _, item := range in {
		p, err := cleanMountPath(item.Path)
		if err != nil {
			return nil, err
		}
		if _, ok := seenPath[p]; ok {
			return nil, invalid("duplicate mount path " + p)
		}
		seenPath[p] = struct{}{}
		vol, err := m.GetVolume(ctx, actor, item.VolumeID)
		if err != nil {
			return nil, err
		}
		if vol.Backend != backend {
			return nil, invalid("volume backend does not match the sandbox")
		}
		out = append(out, store.Mount{VolumeID: vol.ID, Path: p, Source: vol.DockerName})
	}
	return out, nil
}

func validVolumeName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return invalid("volume name must be 1 to 64 characters")
	}
	for i, r := range name {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && !alnum {
			return invalid("volume name must start with a letter or digit")
		}
		if !alnum && r != '.' && r != '_' && r != '-' {
			return invalid("volume name has an invalid character")
		}
	}
	return nil
}

func cleanMountPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || strings.ContainsRune(p, ':') || !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return "", invalid("mount path must be an absolute path without ..")
	}
	c := path.Clean(p)
	switch c {
	case "/", "/tmp", "/opt", "/opt/sbx", "/proc", "/sys", "/dev", "/etc":
		return "", invalid("mount path " + c + " is reserved")
	}
	for _, prefix := range []string{"/tmp/", "/opt/sbx/", "/proc/", "/sys/", "/dev/", "/etc/"} {
		if strings.HasPrefix(c, prefix) {
			return "", invalid("mount path " + c + " is reserved")
		}
	}
	if c != "/workspace" && strings.HasPrefix(c, "/workspace/") {
		return "", invalid("mount /workspace itself to persist the workspace; " + c + " is under the workspace tmpfs")
	}
	return c, nil
}

func driverMounts(mounts []store.Mount) []driver.Mount {
	out := make([]driver.Mount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, driver.Mount{Source: m.Source, Target: m.Path})
	}
	return out
}
