package core

import (
	"context"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

// hookDriver runs during before an exec completes, standing in for a
// concurrent request that lands while the exec is in flight.
type hookDriver struct {
	*fake.Driver
	during func()
}

func (d *hookDriver) Exec(ctx context.Context, id string, req driver.ExecRequest) (driver.ExecResult, error) {
	if f := d.during; f != nil {
		d.during = nil
		f()
	}
	return d.Driver.Exec(ctx, id, req)
}

func TestExecDoesNotUndoConcurrentChanges(t *testing.T) {
	st := openStore(t)
	hd := &hookDriver{Driver: fake.New("docker")}
	mgr := New(config.Default(), st, map[string]driver.Driver{"docker": hd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)

	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", TTLSeconds: intPtr(3600)})
	if err != nil {
		t.Fatal(err)
	}
	var extended time.Time
	hd.during = func() {
		got, err := mgr.SetTTL(ctx, actor, sb.ID, 60)
		if err != nil {
			t.Fatal(err)
		}
		extended = got.ExpiresAt.Truncate(time.Millisecond)
	}
	if _, err := mgr.Exec(ctx, actor, sb.ID, ExecInput{Cmd: []string{"echo", "x"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSandbox(ctx, sb.ID)
	if err != nil || !got.ExpiresAt.Equal(extended) {
		t.Fatalf("ttl change was undone: %v want %v (%v)", got.ExpiresAt, extended, err)
	}

	hd.during = func() {
		if _, err := mgr.Delete(ctx, actor, sb.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = mgr.Exec(ctx, actor, sb.ID, ExecInput{Cmd: []string{"echo", "x"}})
	got, err = st.GetSandbox(ctx, sb.ID)
	if err != nil || got.Status != "stopped" || got.FinishedAt == nil {
		t.Fatalf("deleted sandbox came back: %+v %v", got, err)
	}
	if n, _ := st.CountActive(ctx, actor.KeyID); n != 0 {
		t.Fatalf("deleted sandbox still counts toward quota: %d", n)
	}
	if _, err := mgr.SetTTL(ctx, actor, sb.ID, 60); err == nil {
		t.Fatal("expected conflict on a stopped sandbox")
	}
}
