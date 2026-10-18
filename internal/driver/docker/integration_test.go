package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
)

func TestIntegrationLifecycle(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "docker", ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sbID := id.Sandbox()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: sbID, Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
		Network: driver.NetNone, Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })

	raw, err := exec.Command("docker", "inspect", h.ID).Output()
	if err != nil {
		t.Fatal(err)
	}
	var inspected []struct {
		HostConfig struct {
			CapDrop        []string
			NetworkMode    string
			ReadonlyRootfs bool
			Privileged     bool
			Memory         int64
			NanoCpus       int64
		}
		Config struct {
			User string
		}
	}
	if err := json.Unmarshal(raw, &inspected); err != nil {
		t.Fatal(err)
	}
	hc := inspected[0].HostConfig
	if hc.Privileged || !hc.ReadonlyRootfs || hc.NetworkMode != "none" || len(hc.CapDrop) == 0 || hc.CapDrop[0] != "ALL" {
		t.Fatalf("host config %+v", hc)
	}
	if inspected[0].Config.User != "1000:1000" || hc.Memory != 64*1024*1024 || hc.NanoCpus != 500_000_000 {
		t.Fatalf("user %s mem %d cpu %d", inspected[0].Config.User, hc.Memory, hc.NanoCpus)
	}

	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "hello-sbx"}, TimeoutSeconds: 20})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "hello-sbx\n" {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"id"}, TimeoutSeconds: 20})
	if err != nil || !strings.Contains(string(res.Stdout), "uid=1000") {
		t.Fatalf("id %q %v", res.Stdout, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"ls", "/sys/class/net"}, TimeoutSeconds: 20})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "lo" {
		t.Fatalf("net %q %v", res.Stdout, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"touch", "/etc/sbx-nope"}, TimeoutSeconds: 20})
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("expected rootfs write to fail: %+v %v", res, err)
	}
	if err := d.WriteFile(ctx, h.ID, "/workspace/hello.txt", []byte("from-sbx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadFile(ctx, h.ID, "/workspace/hello.txt", 1<<20)
	if err != nil || string(got) != "from-sbx\n" {
		t.Fatalf("read %q %v", got, err)
	}
	entries, err := d.ListFiles(ctx, h.ID, "/workspace")
	if err != nil || len(entries) != 1 || entries[0].Name != "hello.txt" || entries[0].Type != driver.FileFile {
		t.Fatalf("%+v %v", entries, err)
	}
	var streamed bytes.Buffer
	res, err = d.ExecStream(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "streamed"}, TimeoutSeconds: 20}, func(chunk driver.ExecChunk) error {
		if chunk.Kind == driver.StreamStdout {
			streamed.Write(chunk.Data)
		}
		return nil
	})
	if err != nil || res.ExitCode != 0 || streamed.String() != "streamed\n" {
		t.Fatalf("stream %q %+v %v", streamed.String(), res, err)
	}
	res, err = d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sleep", "5"}, TimeoutSeconds: 1})
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("timeout %+v %v", res, err)
	}
	if err := d.Pause(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	st, err := d.Inspect(ctx, h.ID)
	if err != nil || st.State != driver.StatePaused {
		t.Fatalf("%+v %v", st, err)
	}
	if err := d.Resume(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(ctx, h.ID, "/workspace/hello.txt"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Inspect(ctx, h.ID); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestIntegrationGVisorUnavailable(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "gvisor", Runtime: "runsc", ReadOnlyRootfs: true, UID: 1000, GID: 1000})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = d.Create(ctx, driver.SandboxSpec{ID: id.Sandbox(), Image: "alpine:3.20", CPU: 0.25, MemoryMB: 64, DiskMB: 32, PIDs: 32})
	if !errors.Is(err, driver.ErrRuntimeUnavailable) {
		info, infoErr := d.client.Info(ctx)
		if infoErr == nil {
			if _, ok := info.Runtimes["runsc"]; ok {
				t.Skip("runsc is installed; skipping the missing-runtime assertion")
			}
		}
		t.Fatal(err)
	}
}
