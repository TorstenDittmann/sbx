package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

// ExportFilesystem returns a tar archive of image's root filesystem, pulling
// it with auth when the daemon does not have it.
// The caller must Close the reader, which also deletes the temporary container.
func (c *Client) ExportFilesystem(ctx context.Context, ref string, auth *driver.RegistryAuth) (io.ReadCloser, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	if _, err := c.ImageInspect(ctx, ref); err != nil {
		if pullErr := c.ImagePull(ctx, ref, auth); pullErr != nil {
			return nil, fmt.Errorf("pull %s: %w", ref, pullErr)
		}
	}
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return nil, err
	}
	name := "sbx-export-" + hex.EncodeToString(buf[:])
	id, err := c.ContainerCreate(ctx, name, createRequest{
		Image:      ref,
		Entrypoint: []string{"/bin/true"},
		HostConfig: hostConfig{NetworkMode: "none"},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+c.path("/containers/"+id+"/export"), nil)
	if err != nil {
		_ = c.ContainerRemove(context.Background(), id)
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		_ = c.ContainerRemove(context.Background(), id)
		return nil, err
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = c.ContainerRemove(context.Background(), id)
		return nil, statusErr(resp.StatusCode, raw)
	}
	return &exportBody{ReadCloser: resp.Body, id: id, client: c}, nil
}

type exportBody struct {
	io.ReadCloser
	id     string
	client *Client
	closed bool
}

func (b *exportBody) Close() error {
	err := b.ReadCloser.Close()
	if !b.closed {
		b.closed = true
		_ = b.client.ContainerRemove(context.Background(), b.id)
	}
	return err
}
