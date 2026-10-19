package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// apiClient talks to one Firecracker process over its Unix API socket.
type apiClient struct {
	http *http.Client
}

func newAPI(sock string) *apiClient {
	return &apiClient{http: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}}
}

func (c *apiClient) put(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPut, path, body)
}

func (c *apiClient) do(ctx context.Context, method, path string, body any) error {
	var buf io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		buf = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+path, buf)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		if len(raw) == 0 {
			return fmt.Errorf("firecracker %s %s: status %d", method, path, resp.StatusCode)
		}
		return fmt.Errorf("firecracker %s %s: %s", method, path, bytes.TrimSpace(raw))
	}
	return nil
}

type machineBody struct {
	VCPU int  `json:"vcpu_count"`
	Mem  int  `json:"mem_size_mib"`
	SMT  bool `json:"smt"`
}

type bootBody struct {
	Kernel string `json:"kernel_image_path"`
	Args   string `json:"boot_args"`
}

type driveBody struct {
	ID   string `json:"drive_id"`
	Path string `json:"path_on_host"`
	Root bool   `json:"is_root_device"`
	RO   bool   `json:"is_read_only"`
}

type nicBody struct {
	ID  string `json:"iface_id"`
	MAC string `json:"guest_mac"`
	Tap string `json:"host_dev_name"`
}

type vsockBody struct {
	CID  uint32 `json:"guest_cid"`
	Path string `json:"uds_path"`
}

type actionBody struct {
	Type string `json:"action_type"`
}

func (c *apiClient) configure(ctx context.Context, m machineBody, boot bootBody, drive driveBody, nic *nicBody, vs vsockBody) error {
	if err := c.put(ctx, "/machine-config", m); err != nil {
		return err
	}
	if err := c.put(ctx, "/boot-source", boot); err != nil {
		return err
	}
	if err := c.put(ctx, "/drives/rootfs", drive); err != nil {
		return err
	}
	if nic != nil {
		if err := c.put(ctx, "/network-interfaces/eth0", nic); err != nil {
			return err
		}
	}
	return c.put(ctx, "/vsock", vs)
}

func (c *apiClient) start(ctx context.Context) error {
	return c.put(ctx, "/actions", actionBody{Type: "InstanceStart"})
}

func (c *apiClient) setState(ctx context.Context, state string) error {
	return c.do(ctx, http.MethodPatch, "/vm", map[string]string{"state": state})
}

func waitUnix(ctx context.Context, path string) error {
	deadline := time.Now().Add(5 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	var last error
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		last = err
		if ctx.Err() != nil || time.Now().After(deadline) {
			return fmt.Errorf("socket %s: %w", path, last)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
