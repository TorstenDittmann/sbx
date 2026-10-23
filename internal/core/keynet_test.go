package core

import (
	"context"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func keyActor(t *testing.T, st *store.Store, network store.KeyNetwork) Actor {
	t.Helper()
	_, hash, prefix := id.Key()
	key := store.APIKey{ID: id.RecordID(), Name: "net", Hash: hash, Prefix: prefix, Scopes: []string{"sandbox"}, Network: network, CreatedAt: time.Now().UTC()}
	if err := st.CreateKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return Actor{KeyID: key.ID}
}

func TestKeyMaxModeIsACeiling(t *testing.T) {
	cfg := config.Default()
	cfg.Network.DefaultMode = "full"
	cfg.Limits.MaxSandboxesPerKey = 10
	st := openStore(t)
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	ctx := context.Background()

	offline := keyActor(t, st, store.KeyNetwork{MaxMode: "none"})
	if _, err := mgr.Create(ctx, offline, CreateInput{Image: "alpine:3.20", Network: "full"}); !isStatus(err, 403) {
		t.Fatalf("full under max_mode none = %v", err)
	}
	sb, err := mgr.Create(ctx, offline, CreateInput{Image: "alpine:3.20"})
	if err != nil || sb.Network != "none" {
		t.Fatalf("server default not narrowed: %+v %v", sb, err)
	}

	list := []string{"example.com", "203.0.113.0/24"}
	limited := keyActor(t, st, store.KeyNetwork{MaxMode: "allow", Allow: &list})
	if _, err := mgr.Create(ctx, limited, CreateInput{Image: "alpine:3.20", Network: "full"}); !isStatus(err, 403) {
		t.Fatalf("full under max_mode allow = %v", err)
	}
	if _, err := mgr.Create(ctx, limited, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{"evil.example"}}); !isStatus(err, 403) {
		t.Fatalf("allow outside the key list = %v", err)
	}
	sb, err = mgr.Create(ctx, limited, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{"EXAMPLE.com", "203.0.113.7"}})
	if err != nil || sb.Network != "allow" {
		t.Fatalf("allow inside the key list: %+v %v", sb, err)
	}
	sb, err = mgr.Create(ctx, limited, CreateInput{Image: "alpine:3.20"})
	if err != nil || sb.Network != "allow" || !sameStrings(sb.Allow, list) {
		t.Fatalf("default under max_mode allow: %+v %v", sb, err)
	}

	open := keyActor(t, st, store.KeyNetwork{Mode: "none"})
	if sb, err := mgr.Create(ctx, open, CreateInput{Image: "alpine:3.20", Network: "full"}); err != nil || sb.Network != "full" {
		t.Fatalf("a key mode without max_mode stays a default: %+v %v", sb, err)
	}
}

func TestWarmClaimRespectsKeyNetwork(t *testing.T) {
	cfg := config.Default()
	cfg.Limits.MaxSandboxesPerKey = 5
	st := openStore(t)
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fake.New("docker")}, discard())
	ctx := context.Background()
	if _, err := mgr.CreateTemplate(ctx, Actor{Admin: true}, TemplateInput{Name: "net", Image: "alpine:3.20", Network: "full", Warm: 1}); err != nil {
		t.Fatal(err)
	}
	pool, err := st.ListPool(ctx, "net")
	if err != nil || len(pool) != 1 {
		t.Fatalf("%+v %v", pool, err)
	}

	deny := []string{"203.0.113.9"}
	strict := keyActor(t, st, store.KeyNetwork{Deny: &deny})
	sb, err := mgr.Create(ctx, strict, CreateInput{Template: "net"})
	if err != nil {
		t.Fatal(err)
	}
	if sb.ID == pool[0].ID || !sameStrings(sb.Deny, deny) {
		t.Fatalf("claimed a pooled sandbox without the key deny list: %+v", sb)
	}

	offline := keyActor(t, st, store.KeyNetwork{MaxMode: "none"})
	if _, err := mgr.Create(ctx, offline, CreateInput{Template: "net"}); !isStatus(err, 403) {
		t.Fatalf("max_mode none claimed a full template: %v", err)
	}

	plain := coreActor(t, st)
	sb, err = mgr.Create(ctx, plain, CreateInput{Template: "net"})
	if err != nil || sb.ID != pool[0].ID {
		t.Fatalf("matching key did not claim the pool: %+v %v", sb, err)
	}
}

func isStatus(err error, status int) bool {
	var op *OpError
	return asOp(err, &op) && op.Status == status
}
