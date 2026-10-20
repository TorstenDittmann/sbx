package core

import (
	"context"
	"errors"
	"fmt"
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
	vol, _, err := m.createVolume(ctx, actor, name, backend, false)
	return vol, err
}

// EnsureVolume returns the caller's volume with this name, creating it when it
// does not exist. A retried job can call it again and get the same volume.
// created reports whether this call made the volume.
func (m *Manager) EnsureVolume(ctx context.Context, actor Actor, name, backend string) (vol store.Volume, created bool, err error) {
	return m.createVolume(ctx, actor, name, backend, true)
}

func (m *Manager) createVolume(ctx context.Context, actor Actor, name, backend string, reuse bool) (store.Volume, bool, error) {
	if err := validVolumeName(name); err != nil {
		return store.Volume{}, false, err
	}
	if backend == "" {
		backend = m.cfg.Backend
	}
	d, ok := m.drivers[backend]
	if !ok {
		return store.Volume{}, false, invalid("unknown backend " + backend)
	}
	ops, ok := d.(driver.VolumeOps)
	if !ok {
		return store.Volume{}, false, unsupported("backend does not support volumes")
	}
	if vol, err := m.existingVolume(ctx, actor, name, backend, reuse); err != nil || vol.ID != "" {
		return vol, false, err
	}
	volID := id.Volume()
	vol := store.Volume{
		ID: volID, Name: name, OwnerKeyID: actor.KeyID, Backend: backend,
		DockerName: "sbx-" + strings.TrimPrefix(volID, "vol_"),
		CreatedAt:  m.now(),
	}
	if err := ops.CreateVolume(ctx, vol.DockerName); err != nil {
		return store.Volume{}, false, m.asDriver(err)
	}
	if err := m.store.InsertVolume(ctx, vol); err != nil {
		_ = ops.DeleteVolume(context.Background(), vol.DockerName)
		if strings.Contains(err.Error(), "UNIQUE") {
			// A concurrent create won the name between the lookup and the insert.
			if prev, err := m.existingVolume(ctx, actor, name, backend, reuse); err != nil || prev.ID != "" {
				return prev, false, err
			}
			return store.Volume{}, false, conflict(fmt.Sprintf("volume %q already exists", name))
		}
		return store.Volume{}, false, err
	}
	return vol, true, nil
}

// existingVolume looks up the caller's volume by name. It returns an empty
// volume when there is none, the volume when reuse is set, and a conflict
// that names the existing id otherwise.
func (m *Manager) existingVolume(ctx context.Context, actor Actor, name, backend string, reuse bool) (store.Volume, error) {
	vol, err := m.store.GetVolumeByName(ctx, actor.KeyID, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Volume{}, nil
	}
	if err != nil {
		return store.Volume{}, err
	}
	if !reuse {
		return store.Volume{}, conflict(fmt.Sprintf("volume %q already exists as %s; delete it, or create with if_exists=reuse to get it back", name, vol.ID))
	}
	if vol.Backend != backend {
		return store.Volume{}, conflict(fmt.Sprintf("volume %q already exists on backend %s, not %s", name, vol.Backend, backend))
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
	// Create inserts its row under m.mu after checking that its volumes still
	// exist. Holding m.mu here keeps a create from slipping between this
	// in-use check and the delete, which would let Docker recreate the volume
	// empty under the same name.
	m.mu.Lock()
	defer m.mu.Unlock()
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

// mountsStillExist re-checks mounted volumes. The caller holds m.mu, so a
// concurrent DeleteVolume has either finished or not started.
func (m *Manager) mountsStillExist(ctx context.Context, mounts []store.Mount) error {
	for _, mt := range mounts {
		if _, err := m.store.GetVolume(ctx, mt.VolumeID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return notFound("volume not found")
			}
			return err
		}
	}
	return nil
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
