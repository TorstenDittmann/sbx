package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/probe"
	"github.com/TorstenDittmann/sbx/internal/setup"
)

// TestIntegrationBoot boots a real microVM when SBX_FC_INTEGRATION=1.
// GitHub-hosted runners have /dev/kvm but are not root, so CI runs this
// under sudo. The test downloads the pinned Firecracker binary and kernel,
// builds an ext4 rootfs from alpine, and logs startup_ms until the guest
// agent answers.
func TestIntegrationBoot(t *testing.T) {
	if os.Getenv("SBX_FC_INTEGRATION") == "" {
		t.Skip("set SBX_FC_INTEGRATION=1 to boot Firecracker")
	}
	if ok, detail := probe.KVM(); !ok {
		t.Skip(detail)
	}
	if os.Getuid() != 0 {
		t.Skip("root is required for the tap device and the ext4 mount")
	}
	agentBin := os.Getenv("SBX_AGENT_BIN")
	if agentBin == "" || !probe.File(agentBin) {
		t.Fatal("SBX_AGENT_BIN must point at a static sbx-agent")
	}
	// Firecracker's API socket must stay under the Unix path limit. The
	// default test temp dir on a GitHub runner is long enough to trip it.
	dir := "/tmp/sbx-fc-it"
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if _, err := setup.Firecracker(ctx, setup.Options{DataDir: dir, Apply: true}); err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{
		DataDir:    dir,
		AgentBin:   agentBin,
		DockerHost: "/var/run/docker.sock",
	})
	if err != nil {
		t.Fatal(err)
	}
	const id = "sbx_fcintegration01"
	start := time.Now()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: id, Image: "alpine:3.20", CPU: 1, MemoryMB: 128, DiskMB: 256, PIDs: 64,
		Network: driver.NetNone,
	})
	if err != nil {
		t.Log(consoleLog(dir, id))
		t.Fatal(err)
	}
	t.Logf("startup_ms %d", time.Since(start).Milliseconds())
	defer d.Delete(context.Background(), h.ID)
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "firecracker"}})
	if err != nil || res.ExitCode != 0 {
		t.Log(consoleLog(dir, id))
		t.Fatalf("%+v %v", res, err)
	}
	t.Logf("exec_ms %d stdout %q", time.Since(start).Milliseconds(), res.Stdout)
	if probe.File(filepath.Join(dir, "firecracker", "vmlinux")) == false {
		t.Fatal("kernel missing")
	}
}

func consoleLog(dir, id string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "firecracker", "vms", id, "console.log"))
	if err != nil {
		return "console: " + err.Error()
	}
	return "console:\n" + string(raw)
}
