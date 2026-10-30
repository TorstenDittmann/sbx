package core

import (
	"context"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

func TestHostImageNeedsAdmin(t *testing.T) {
	cfg := config.Default()
	cfg.Builds.AllowSandboxKeys = true
	st := openStore(t)
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)

	var op *OpError
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "ext4:/etc/passwd"}); !asOp(err, &op) || op.Status != 403 {
		t.Fatalf("create: want 403, got %v", err)
	}
	if _, err := mgr.Build(ctx, actor, BuildInput{Image: "ext4:/etc/passwd"}); !asOp(err, &op) || op.Status != 403 {
		t.Fatalf("build: want 403, got %v", err)
	}
	if err := checkHostImage(Actor{KeyID: actor.KeyID, Admin: true}, "ext4:/srv/base.ext4"); err != nil {
		t.Fatal(err)
	}
}
