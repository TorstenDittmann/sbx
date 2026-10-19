package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestReconcileAdoptsAndDrops(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	cfg := config.Default()
	cfg.Limits.MaxSandboxes = 1
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	if !mgr.Ready() {
		// ready flips only after Reconcile
	}
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"}); err == nil {
		t.Fatal("expected node quota")
	}

	fd.Seed(driver.Status{ID: "ctr-adopt", SandboxID: "sbx_adopt000000000", State: driver.StateRunning, Image: "alpine:3.20"})
	adopt := store.Sandbox{
		ID: "sbx_adopt000000000", Image: "alpine:3.20", Backend: "docker", Status: "creating",
		CPU: 1, MemoryMB: 128, DiskMB: 64, PIDs: 32, Network: "none", OwnerKeyID: actor.KeyID,
		CreatedAt: mgr.now(), ExpiresAt: mgr.now().Add(time.Hour), LastActivityAt: mgr.now(),
	}
	if err := st.InsertSandbox(ctx, adopt); err != nil {
		t.Fatal(err)
	}
	lost := store.Sandbox{
		ID: "sbx_lost0000000000", Image: "alpine:3.20", Backend: "docker", Status: "running",
		CPU: 1, MemoryMB: 128, DiskMB: 64, PIDs: 32, Network: "none", OwnerKeyID: actor.KeyID,
		CreatedAt: mgr.now(), ExpiresAt: mgr.now().Add(time.Hour), LastActivityAt: mgr.now(), ContainerID: "gone",
	}
	if err := st.InsertSandbox(ctx, lost); err != nil {
		t.Fatal(err)
	}
	fd.Seed(driver.Status{ID: "orphan-ctr", SandboxID: "sbx_orphan00000000", State: driver.StateRunning})

	if err := mgr.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !mgr.Ready() {
		t.Fatal("expected ready")
	}
	got, err := st.GetSandbox(ctx, adopt.ID)
	if err != nil || got.Status != "running" || got.ContainerID != "ctr-adopt" {
		t.Fatalf("adopt %+v %v", got, err)
	}
	got, err = st.GetSandbox(ctx, lost.ID)
	if err != nil || got.Status != "stopped" || got.Error != "lost" {
		t.Fatalf("lost %+v %v", got, err)
	}
	if _, err := fd.Inspect(ctx, "orphan-ctr"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatal("orphan should be deleted", err)
	}
	if _, err := fd.Inspect(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileLeavesRowsWhenListFails(t *testing.T) {
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	fd.ListErr = errors.New("docker down")
	if err := mgr.Reconcile(ctx); err == nil || !strings.Contains(err.Error(), "docker down") {
		t.Fatal(err)
	}
	got, err := st.GetSandbox(ctx, sb.ID)
	if err != nil || got.Status != "running" || got.Error != "" {
		t.Fatalf("row changed %+v %v", got, err)
	}
}
