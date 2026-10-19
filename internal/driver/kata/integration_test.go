package kata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

// TestIntegrationBoot starts a real Kata sandbox when SBX_KATA_INTEGRATION=1.
// GitHub-hosted runners expose /dev/kvm on some images; the CI job skips when
// the runtime class is not registered.
func TestIntegrationBoot(t *testing.T) {
	if os.Getenv("SBX_KATA_INTEGRATION") == "" {
		t.Skip("set SBX_KATA_INTEGRATION=1 to boot a Kata sandbox")
	}
	host := os.Getenv("SBX_DOCKER_HOST")
	if host == "" {
		host = "/var/run/docker.sock"
	}
	d, err := New(Options{
		Host: host, ReadOnlyRootfs: true, UID: 1000, GID: 1000,
		AgentBin: os.Getenv("SBX_AGENT_BIN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := d.Check(ctx); err != nil {
		t.Skip("kata runtime is not ready: " + err.Error())
	}
	start := time.Now()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: "sbx_kataintegration1", Image: "alpine:3.20", CPU: 1, MemoryMB: 256, DiskMB: 512, PIDs: 128,
		Network: driver.NetNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "kata"}, TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("kata create+exec %s exit=%d stdout=%q", elapsed, res.ExitCode, res.Stdout)
	fmt.Printf("KATA_BOOT_MS %d\n", elapsed.Milliseconds())
	if res.ExitCode != 0 {
		t.Fatalf("exec failed: %s", res.Stderr)
	}
}
