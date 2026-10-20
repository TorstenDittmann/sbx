package core

import (
	"context"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

func TestPreviewsSurviveRestart(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	drivers := map[string]driver.Driver{"docker": fd}
	mgr := New(config.Default(), st, drivers, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	pv, err := mgr.CreatePreview(ctx, actor, sb.ID, 3000, false)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreatePreview(ctx, actor, gone.ID, 8080, false); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Delete(ctx, actor, gone.ID); err != nil {
		t.Fatal(err)
	}

	restarted := New(config.Default(), st, drivers, discard())
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, ok := restarted.PreviewByPort(sb.ID, 3000)
	if !ok || got.ID != pv.ID {
		t.Fatalf("preview lost on restart: %+v %v", got, ok)
	}
	if !restarted.PreviewTokenOK(got, pv.Token) {
		t.Fatal("preview token stopped working after restart")
	}
	if _, ok := restarted.PreviewByPort(gone.ID, 8080); ok {
		t.Fatal("preview of a deleted sandbox came back")
	}
	if err := restarted.DeletePreview(ctx, actor, sb.ID, pv.ID); err != nil {
		t.Fatal(err)
	}
	again := New(config.Default(), st, drivers, discard())
	if err := again.LoadPreviews(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := again.PreviewByPort(sb.ID, 3000); ok {
		t.Fatal("deleted preview came back")
	}
}
