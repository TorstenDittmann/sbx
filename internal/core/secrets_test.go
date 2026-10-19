package core

import (
	"context"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

func TestSecretsStayOutOfExecEnvMerge(t *testing.T) {
	cfg := config.Default()
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)

	if _, err := mgr.Create(ctx, actor, CreateInput{
		Image: "alpine:3.20", Env: map[string]string{"FOO": "bar"}, Secrets: map[string]string{"FOO": "nope"},
	}); err == nil {
		t.Fatal("expected overlap to fail")
	}

	sb, err := mgr.Create(ctx, actor, CreateInput{
		Image: "alpine:3.20", TTLSeconds: intPtr(60),
		Env: map[string]string{"FOO": "bar"}, Secrets: map[string]string{"TOKEN": "sekret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Env["TOKEN"] != "" || sb.Secrets["TOKEN"] != "sekret" || sb.Env["FOO"] != "bar" {
		t.Fatalf("%+v", sb)
	}
	spec, ok := fd.Spec(sb.ID)
	if !ok || spec.Env["TOKEN"] != "sekret" || spec.Env["FOO"] != "bar" {
		t.Fatalf("container env %+v", spec.Env)
	}
	if _, err := mgr.Exec(ctx, actor, sb.ID, ExecInput{Cmd: []string{"echo", "hi"}, Env: map[string]string{"TOKEN": "override"}}); err != nil {
		t.Fatal(err)
	}
	calls := fd.Execs()
	if len(calls) != 1 || calls[0].Env["TOKEN"] != "override" || calls[0].Env["FOO"] != "" {
		t.Fatalf("%+v", calls)
	}
	updated, err := mgr.SetSecrets(ctx, actor, sb.ID, map[string]string{"OTHER": "later"})
	if err != nil || updated.Secrets["TOKEN"] != "" || updated.Secrets["OTHER"] != "later" {
		t.Fatalf("%+v %v", updated.Secrets, err)
	}
	if _, err := mgr.SetSecrets(ctx, actor, sb.ID, map[string]string{"FOO": "x"}); err == nil {
		t.Fatal("expected env overlap")
	}
}
