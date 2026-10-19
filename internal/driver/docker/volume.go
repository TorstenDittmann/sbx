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
func (d *Driver) chownMount(ctx context.Context, source string) error {
	uid, gid := d.opt.UID, d.opt.GID
	if uid == 0 {
		uid = 1000
	}
	if gid == 0 {
		gid = uid
	}
	cmd := []string{fmt.Sprintf("%d:%d", uid, gid), "/mnt"}
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	body := createRequest{
		Image:      "alpine:3.20",
		User:       "0:0",
		Entrypoint: []string{"/bin/chown"},
		Cmd:        &cmd,
		HostConfig: hostConfig{
			NetworkMode:    "none",
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			ReadonlyRootfs: false,
			Memory:         32 * 1024 * 1024,
			MemorySwap:     32 * 1024 * 1024,
			NanoCpus:       500_000_000,
			PidsLimit:      32,
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
