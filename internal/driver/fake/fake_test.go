package fake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestEdges(t *testing.T) {
	ctx := context.Background()
	d := New("")
	if d.Name() != "docker" || d.Capabilities().IsolationLevel != "container" {
		t.Fatal(d.Capabilities())
	}
	if err := d.EnsureImage(ctx, "alpine:3.20"); err != nil {
		t.Fatal(err)
	}
	d.SetPause(false)
	if d.Capabilities().Pause {
		t.Fatal("pause should be off")
	}
	h, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_fake0000000001", Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Pause(ctx, h.ID); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatal(err)
	}
	d.SetPause(true)
	if err := d.Pause(ctx, "missing"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if err := d.Resume(ctx, "missing"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := d.ListFiles(ctx, "missing", "/workspace"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := d.ReadFile(ctx, h.ID, "/workspace/nope", 10); err == nil {
		t.Fatal("expected missing file")
	}
	if err := d.WriteFile(ctx, h.ID, "/workspace/dir/a.txt", []byte("hello-world"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := d.ListFiles(ctx, h.ID, "/workspace")
	if err != nil || len(entries) != 1 || entries[0].Type != driver.FileDir {
		t.Fatalf("%+v %v", entries, err)
	}
	got, err := d.ReadFile(ctx, h.ID, "/workspace/dir/a.txt", 4)
	if err == nil || string(got) != "hell" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := d.Exec(ctx, "missing", driver.ExecRequest{Cmd: []string{"echo", "x"}}); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal(err)
	}
	gv := New("gvisor")
	if gv.Name() != "gvisor" || gv.Capabilities().IsolationLevel != "gvisor" {
		t.Fatal(gv.Capabilities())
	}
	d.CreateErr = errors.New("nope")
	if _, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_fake0000000002", Image: "alpine:3.20"}); err == nil {
		t.Fatal("expected create error")
	}
}
