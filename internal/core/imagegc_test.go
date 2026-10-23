package core

import (
	"context"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestCollectImagesKeepsReferencedAndRecent(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mgr.SetClock(func() time.Time { return now })
	ctx := context.Background()
	admin := Actor{KeyID: coreActor(t, st).KeyID, Admin: true}

	tpl, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{Name: "kept", Image: "node:22", Steps: []store.BuildStep{{Run: "npm i -g pnpm"}}})
	if err != nil {
		t.Fatal(err)
	}
	tplImage := buildTag(tpl.Image, renderSteps(tpl.Steps))
	live, err := mgr.Create(ctx, admin, CreateInput{Image: "alpine:3.20", Steps: []store.BuildStep{{Run: "echo live"}}})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := mgr.Create(ctx, admin, CreateInput{Image: "alpine:3.20", Steps: []store.BuildStep{{Run: "echo gone"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Delete(ctx, admin, gone.ID); err != nil {
		t.Fatal(err)
	}
	recent, err := mgr.Build(ctx, admin, BuildInput{Image: "alpine:3.20", Steps: []store.BuildStep{{Run: "echo recent"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Built before usage was recorded, so its build time counts.
	fd.AddImage("sbx-template:legacy", now.Add(-30*24*time.Hour))
	fd.AddImage("sbx-template:fresh", now.Add(-time.Hour))
	// Created from by tag, without steps.
	fd.AddImage("sbx-template:0123456789abcdef0123456789abcdef", now.Add(-time.Hour))

	if n, err := mgr.CollectImages(ctx); err != nil || n != 1 {
		t.Fatalf("first pass removed %d, err %v", n, err)
	}
	if has(t, fd, "sbx-template:legacy") || !has(t, fd, gone.Image) {
		t.Fatal("first pass should remove only the legacy image")
	}

	// A week later, the deleted sandbox's image and the ahead-of-time build
	// have aged out. A sandbox reusing the build refreshes it.
	now = now.Add(7*24*time.Hour + time.Minute)
	if _, err := mgr.Build(ctx, admin, BuildInput{Image: "alpine:3.20", Steps: []store.BuildStep{{Run: "echo recent"}}}); err != nil {
		t.Fatal(err)
	}
	bytag, err := mgr.Create(ctx, admin, CreateInput{Image: "sbx-template:0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Delete(ctx, admin, bytag.ID); err != nil {
		t.Fatal(err)
	}
	n, err := mgr.CollectImages(ctx)
	if err != nil || n != 2 {
		t.Fatalf("second pass removed %d, err %v", n, err)
	}
	for ref, want := range map[string]bool{
		tplImage: true, live.Image: true, recent.Image: true,
		"sbx-template:0123456789abcdef0123456789abcdef": true,
		gone.Image: false, "sbx-template:fresh": false,
	} {
		if has(t, fd, ref) != want {
			t.Errorf("%s present = %v, want %v", ref, !want, want)
		}
	}
	if mgr.Counts.ImagesGCed.Load() != 3 {
		t.Fatal(mgr.Counts.ImagesGCed.Load())
	}

	// Deleting the template releases its image once it is old enough.
	if err := mgr.DeleteTemplate(ctx, "kept"); err != nil {
		t.Fatal(err)
	}
	if n, err := mgr.CollectImages(ctx); err != nil || n != 1 || has(t, fd, tplImage) {
		t.Fatalf("template pass removed %d, err %v", n, err)
	}
}

func TestCollectImagesDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Builds.RetainUnusedSeconds = 0
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	fd.AddImage("sbx-template:old", time.Unix(0, 0))
	if n, err := mgr.CollectImages(context.Background()); err != nil || n != 0 || !has(t, fd, "sbx-template:old") {
		t.Fatalf("removed %d, err %v", n, err)
	}
}

func has(t *testing.T, fd *fake.Driver, ref string) bool {
	t.Helper()
	ok, err := fd.HasImage(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestCollectImagesKeepsSnapshotTemplates(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mgr.SetClock(func() time.Time { return now })
	ctx := context.Background()
	admin := Actor{KeyID: coreActor(t, st).KeyID, Admin: true}
	sb, err := mgr.Create(ctx, admin, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	named, err := mgr.Snapshot(ctx, admin, sb.ID, SnapshotInput{Template: "prepared"})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := mgr.Snapshot(ctx, admin, sb.ID, SnapshotInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Delete(ctx, admin, sb.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Duration(cfg.Builds.RetainUnusedSeconds+60) * time.Second)
	if n, err := mgr.CollectImages(ctx); err != nil || n != 1 {
		t.Fatalf("removed %d, err %v", n, err)
	}
	if !has(t, fd, named.Image) || has(t, fd, loose.Image) {
		t.Fatal("the template's snapshot must stay and the loose one go")
	}
}
