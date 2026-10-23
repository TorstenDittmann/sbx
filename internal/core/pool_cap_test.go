package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

func TestNodeCapCountsWarmPool(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxSandboxes = 2
	st := openStore(t)
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	if _, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{Name: "pool", Image: "alpine:3.20", Warm: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"}); !isStatus(err, 429) {
		t.Fatalf("cold create past the node cap = %v", err)
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Template: "pool"}); err != nil {
		t.Fatalf("a claim adds no container and must pass the cap: %v", err)
	}
	if n, _ := st.CountAllActive(ctx); n > 2 {
		t.Fatalf("%d live sandboxes over a cap of 2", n)
	}
}

func TestConcurrentReplenishDoesNotOverfill(t *testing.T) {
	st := openStore(t)
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	ctx := context.Background()
	if _, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{Name: "pool", Image: "alpine:3.20", Warm: 2}); err != nil {
		t.Fatal(err)
	}
	mgr.drainPool(ctx, "pool")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.Replenish(ctx)
		}()
	}
	wg.Wait()
	if n, err := st.CountWarm(ctx, "pool", "docker"); err != nil || n != 2 {
		t.Fatalf("warm %d %v, want 2", n, err)
	}
}

type racingVolumes struct {
	*fake.Driver
	during func()
}

func (d *racingVolumes) DeleteVolume(ctx context.Context, name string) error {
	if f := d.during; f != nil {
		d.during = nil
		f()
	}
	return d.Driver.DeleteVolume(ctx, name)
}

func TestCreateCannotMountAVolumeBeingDeleted(t *testing.T) {
	st := openStore(t)
	rv := &racingVolumes{Driver: fake.New("docker")}
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": rv}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	vol, err := mgr.CreateVolume(ctx, actor, "cache", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	rv.during = func() {
		go func() {
			_, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Mounts: []MountInput{{VolumeID: vol.ID, Path: "/workspace"}}})
			done <- err
		}()
		// Give the create time to get past its mount lookup.
		time.Sleep(50 * time.Millisecond)
	}
	if err := mgr.DeleteVolume(ctx, actor, vol.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !isStatus(err, 404) {
		t.Fatalf("create mounted a deleted volume: %v", err)
	}
}
