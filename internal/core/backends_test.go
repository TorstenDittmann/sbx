package core

import (
	"context"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

func TestTemplateBackendAndCatalog(t *testing.T) {
	st := openStore(t)
	dock := fake.New("docker")
	gvis := fake.New("gvisor")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": dock, "gvisor": gvis}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	if err := mgr.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	tpl, err := mgr.CreateTemplate(ctx, TemplateInput{Name: "vm", Image: "alpine:3.20", Backend: "gvisor"})
	if err != nil || tpl.Backend != "gvisor" {
		t.Fatalf("%+v %v", tpl, err)
	}
	if _, err := mgr.CreateTemplate(ctx, TemplateInput{Name: "nope", Image: "alpine:3.20", Backend: "kata"}); err == nil {
		t.Fatal("expected unknown backend")
	}
	sb, err := mgr.Create(ctx, actor, CreateInput{Template: "vm"})
	if err != nil || sb.Backend != "gvisor" || sb.Image != "alpine:3.20" {
		t.Fatalf("%+v %v", sb, err)
	}
	explicit, err := mgr.Create(ctx, actor, CreateInput{Template: "vm", Backend: "docker"})
	if err != nil || explicit.Backend != "docker" {
		t.Fatalf("%+v %v", explicit, err)
	}
	rows := mgr.ListBackends(ctx)
	if len(rows) != 4 || rows[0].Name != "docker" || !rows[0].Available || rows[2].Name != "kata" || rows[2].Available {
		t.Fatalf("%+v", rows)
	}
	if rows[1].Name != "gvisor" || !rows[1].Available || rows[3].Name != "firecracker" {
		t.Fatalf("%+v", rows)
	}
}
