package docker

import (
	"context"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestReport(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000}, cli)
	info := d.Report(context.Background())
	if !info.Available || info.Name != "docker" || info.Isolation != "container" || !info.Exec || info.PTY {
		t.Fatalf("%+v", info)
	}
	gv := NewWithClient(Options{Name: "gvisor", Runtime: "runsc", UID: 1000, GID: 1000}, cli)
	missing := gv.Report(context.Background())
	if missing.Available || missing.Isolation != "gvisor" {
		t.Fatalf("%+v", missing)
	}
	fd.mu.Lock()
	fd.runtimes["runsc"] = true
	fd.mu.Unlock()
	ready := gv.Report(context.Background())
	if !ready.Available || ready.Detail == "" {
		t.Fatalf("%+v", ready)
	}
	caps := d.Capabilities()
	if len(caps.Networks) != 2 || caps.Networks[0] != driver.NetNone {
		t.Fatalf("%+v", caps)
	}
}
