package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
)

func TestIntegrationBuildImage(t *testing.T) {
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
	d, err := New(Options{Name: "docker", ReadOnlyRootfs: true, UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tag := "sbx-template:test" + strings.ToLower(id.RecordID())[:8]
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })

	if ok, err := d.HasImage(ctx, tag); err != nil || ok {
		t.Fatalf("before build: %v %v", ok, err)
	}
	err = d.BuildImage(ctx, driver.BuildSpec{
		Tag: tag, Base: "alpine:3.20", TimeoutSeconds: 240,
		Run: []string{
			"export GREETING='hi there'\nmkdir -p '/app' && cd '/app' || exit 1\nprintf '%s' \"$GREETING\" > greeting.txt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.HasImage(ctx, tag); err != nil || !ok {
		t.Fatalf("after build: %v %v", ok, err)
	}

	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: id.Sandbox(), Image: tag, CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64, Network: driver.NetNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", "cat /app/greeting.txt; echo; echo ${GREETING:-unset}; id -u"}, TimeoutSeconds: 20})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "hi there\nunset\n1000\n" {
		t.Fatalf("%q %q %v", res.Stdout, res.Stderr, err)
	}

	err = d.BuildImage(ctx, driver.BuildSpec{Tag: tag + "x", Base: "alpine:3.20", Run: []string{"echo before-fail; exit 3"}})
	if !errors.Is(err, driver.ErrImage) || !strings.Contains(err.Error(), "before-fail") {
		t.Fatalf("err = %v", err)
	}
}
