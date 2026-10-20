package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func (c *Client) VolumeRemove(ctx context.Context, name string) error {
	err := c.DoJSON(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil)
	if err == nil || errors.Is(err, driver.ErrNotFound) {
		return nil
	}
	return err
}

// CreateVolume creates a Docker volume. The caller chooses the name.
func (d *Driver) CreateVolume(ctx context.Context, name string) error {
	return d.client.VolumeCreate(ctx, name, map[string]string{labelManaged: "true"})
}

// DeleteVolume removes a Docker volume. A missing volume is success.
func (d *Driver) DeleteVolume(ctx context.Context, name string) error {
	return d.client.VolumeRemove(ctx, name)
}

func (d *Driver) prepareMounts(ctx context.Context, mounts []driver.Mount) error {
	if len(mounts) == 0 {
		return nil
	}
	if err := d.EnsureImage(ctx, "alpine:3.20"); err != nil {
		return err
	}
	for _, m := range mounts {
		if err := d.chownMount(ctx, m.Source); err != nil {
			return err
		}
	}
	return nil
}

// chownMount makes the volume root writable by the sandbox uid. It does not
// walk the tree, so files already in the volume keep their owners.
//
// The prep container bind-mounts the volume's host mountpoint. A named-volume
// bind is the fallback. Dropped capabilities made chown a no-op on some
// daemons while the container still exited 0, so this process keeps root's
// default capability set. An empty volume also receives .sbx-owned; without a
// child entry the daemon treats the volume as empty and can reset the root
// owner when the sandbox mounts it.
func (d *Driver) chownMount(ctx context.Context, source string) error {
	uid, gid := d.opt.UID, d.opt.GID
	if uid == 0 {
		uid = 1000
	}
	if gid == 0 {
		gid = uid
	}
	targets := []string{source}
	if mp, err := d.client.VolumeMountpoint(ctx, source); err == nil && mp != "" {
		targets = []string{mp, source}
	}
	var last error
	for _, target := range targets {
		last = d.chownPath(ctx, target, uid, gid)
		if last == nil {
			return nil
		}
	}
	return last
}

func (d *Driver) chownPath(ctx context.Context, source string, uid, gid int) error {
	script := fmt.Sprintf(`set -eu
chown %d:%d /mnt
if [ -z "$(ls -A /mnt)" ]; then
  chmod 755 /mnt
  touch /mnt/.sbx-owned
  chown %d:%d /mnt/.sbx-owned
fi
`, uid, gid, uid, gid)
	cmd := []string{script}
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	body := createRequest{
		Image:      "alpine:3.20",
		User:       "0:0",
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        &cmd,
		HostConfig: hostConfig{
			NetworkMode:    "none",
			ReadonlyRootfs: false,
			Memory:         64 * 1024 * 1024,
			MemorySwap:     64 * 1024 * 1024,
			NanoCpus:       500_000_000,
			PidsLimit:      64,
			Binds:          []string{source + ":/mnt"},
		},
	}
	id, err := d.client.ContainerCreate(ctx, "sbx-volprep-"+hex.EncodeToString(nonce[:]), body)
	if err != nil {
		return err
	}
	defer d.client.ContainerRemove(context.Background(), id)
	if err := d.client.ContainerStart(ctx, id); err != nil {
		return err
	}
	code, err := d.client.ContainerWait(ctx, id)
	if err != nil {
		return err
	}
	if code != 0 {
		logs, _ := d.client.ContainerLogs(ctx, id)
		return fmt.Errorf("chown %s exited %d: %s", source, code, logs)
	}
	return nil
}
