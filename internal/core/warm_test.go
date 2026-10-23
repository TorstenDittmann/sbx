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

func TestWarmPoolClaimAndSetup(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxSandboxesPerKey = 3
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)

	fd.ExecCode = 7
	if _, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{
		Name: "bad", Image: "alpine:3.20", Warm: 1, Setup: []string{"echo", "no"},
	}); err == nil {
		t.Fatal("expected setup failure")
	}
	if _, err := st.GetTemplate(ctx, "bad"); err != store.ErrNotFound {
		t.Fatal(err)
	}
	fd.ExecCode = 0

	tpl, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{
		Name: "agent", Image: "alpine:3.20", Description: "ready",
		CPU: 0.5, MemoryMB: 128, DiskMB: 64, Network: "full",
		Setup: []string{"echo", "ready"}, Warm: 1, Env: map[string]string{"FOO": "bar"},
	})
	if err != nil || tpl.Warm != 1 || tpl.Network != "full" {
		t.Fatalf("%+v %v", tpl, err)
	}
	warm, err := st.ListPool(ctx, "agent")
	if err != nil || len(warm) != 1 || warm[0].Status != "warm" || warm[0].Network != "full" {
		t.Fatalf("%+v %v", warm, err)
	}
	if len(fd.Execs()) == 0 || fd.Execs()[len(fd.Execs())-1].Cmd[0] != "echo" {
		t.Fatal(fd.Execs())
	}
	listed, err := mgr.List(ctx, actor, ListInput{})
	if err != nil || len(listed) != 0 {
		t.Fatalf("user list %+v %v", listed, err)
	}
	adminWarm, err := mgr.List(ctx, Actor{Admin: true}, ListInput{Status: "warm"})
	if err != nil || len(adminWarm) != 1 {
		t.Fatalf("admin warm %+v %v", adminWarm, err)
	}
	if got, err := mgr.List(ctx, actor, ListInput{Status: "warm"}); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}

	sb, err := mgr.Create(ctx, actor, CreateInput{Template: "agent", TTLSeconds: intPtr(60), Labels: map[string]string{"user": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if sb.ID != warm[0].ID || sb.Status != "running" || sb.OwnerKeyID != actor.KeyID || sb.Network != "full" || sb.Labels["user"] != "1" {
		t.Fatalf("claimed %+v", sb)
	}
	waitWarm(t, st, "agent", 1)
	before := len(fd.Execs())

	cold, err := mgr.Create(ctx, actor, CreateInput{Template: "agent", MemoryMB: intPtr(256), TTLSeconds: intPtr(60)})
	if err != nil {
		t.Fatal(err)
	}
	if cold.ID == warm[0].ID || cold.MemoryMB != 256 {
		t.Fatalf("override should cold start %+v", cold)
	}
	if len(fd.Execs()) != before+1 {
		t.Fatal(fd.Execs())
	}

	if _, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{Name: "agent", Image: "alpine:3.20"}); err == nil {
		t.Fatal("expected duplicate")
	}
	if _, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{Name: "too-many", Image: "alpine:3.20", Warm: 9}); err == nil {
		t.Fatal("expected warm cap")
	}
	if err := mgr.DeleteTemplate(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	left, err := st.ListPool(ctx, "agent")
	if err != nil || len(left) != 0 {
		t.Fatalf("pool %+v %v", left, err)
	}
	got, err := st.GetSandbox(ctx, sb.ID)
	if err != nil || got.Status != "running" {
		t.Fatalf("user sandbox should remain %+v %v", got, err)
	}
}

func waitWarm(t *testing.T, st *store.Store, template string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, err := st.CountWarm(context.Background(), template, "docker")
		if err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("warm count %d, want %d", n, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
