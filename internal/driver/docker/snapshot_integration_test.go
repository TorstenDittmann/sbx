package docker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
)

func TestIntegrationSnapshot(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "docker", ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dangling := func() int {
		out, err := exec.Command("docker", "image", "ls", "-aq", "--filter", "dangling=true", "--no-trunc").Output()
		if err != nil {
			t.Fatal(err)
		}
		return len(strings.Fields(string(out)))
	}
	before := dangling()
	tag := "sbx-template:" + id.Snapshot()
	tag2 := "sbx-template:" + id.Snapshot()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag, tag2).Run() })

	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64, Network: driver.NetNone,
		Env: map[string]string{"TOKEN": "s3cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	prep := "mkdir -p src/deep && echo hello > src/deep/a.txt && echo private > key && chmod 600 key && ln -s src/deep/a.txt link"
	if res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", prep}, TimeoutSeconds: 20}); err != nil || res.ExitCode != 0 {
		t.Fatalf("prep: %q %v", res.Stderr, err)
	}
	if err := d.Snapshot(ctx, h.ID, driver.SnapshotSpec{Tag: tag, Base: "alpine:3.20", Workspace: true}); err != nil {
		t.Fatal(err)
	}
	if n := dangling(); n != before {
		t.Fatalf("snapshot left %d dangling images", n-before)
	}
	out, err := exec.Command("docker", "image", "inspect", "-f", "{{json .Config.Env}} {{json .Config.Labels}}", tag).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if strings.Contains(string(out), "s3cret") || strings.Contains(string(out), h.ID) {
		t.Fatalf("snapshot config leaks sandbox state: %s", out)
	}

	fork, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: tag, CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64, Network: driver.NetNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), fork.ID) })
	check := "cat src/deep/a.txt; cat key; readlink link; stat -c %a key; echo ${TOKEN:-empty}; id -u; stat -c %u src/deep/a.txt"
	res, err := d.Exec(ctx, fork.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", check}, TimeoutSeconds: 20})
	want := "hello\nprivate\nsrc/deep/a.txt\n600\nempty\n1000\n1000\n"
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != want {
		t.Fatalf("fork: %q %q %v", res.Stdout, res.Stderr, err)
	}

	// A snapshot of the fork drops a file the fork deleted.
	if res, err := d.Exec(ctx, fork.ID, driver.ExecRequest{Cmd: []string{"rm", "key"}, TimeoutSeconds: 20}); err != nil || res.ExitCode != 0 {
		t.Fatalf("rm: %q %v", res.Stderr, err)
	}
	if err := d.Snapshot(ctx, fork.ID, driver.SnapshotSpec{Tag: tag2, Base: tag, Workspace: true}); err != nil {
		t.Fatal(err)
	}
	if n := dangling(); n != before {
		t.Fatalf("second snapshot left %d dangling images", n-before)
	}
	again, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: tag2, CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64, Network: driver.NetNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), again.ID) })
	res, err = d.Exec(ctx, again.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", "cat src/deep/a.txt; ls key 2>/dev/null || echo gone; echo ${TOKEN:-empty}; ls /var/lib/sbx-snapshot"}, TimeoutSeconds: 20})
	if err != nil || string(res.Stdout) != "hello\ngone\nempty\n"+strings.TrimPrefix(tag2, "sbx-template:")+"\n" {
		t.Fatalf("second fork: %q %q %v", res.Stdout, res.Stderr, err)
	}
}

func TestIntegrationSnapshotRootfs(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "docker", Root: true, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tag := "sbx-template:" + id.Snapshot()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64, Network: driver.NetNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	if res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", "mkdir /app && echo built > /app/out && echo ws > w"}, TimeoutSeconds: 20}); err != nil || res.ExitCode != 0 {
		t.Fatalf("prep: %q %v", res.Stderr, err)
	}
	if err := d.Snapshot(ctx, h.ID, driver.SnapshotSpec{Tag: tag, Base: "alpine:3.20", Workspace: true}); err != nil {
		t.Fatal(err)
	}
	fork, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: tag, CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64, Network: driver.NetNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), fork.ID) })
	res, err := d.Exec(ctx, fork.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", "cat /app/out w"}, TimeoutSeconds: 20})
	if err != nil || string(res.Stdout) != "built\nws\n" {
		t.Fatalf("fork: %q %q %v", res.Stdout, res.Stderr, err)
	}
}
