package core

import (
	"context"
	"errors"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func list(v ...string) *[]string { return &v }

func netManager(t *testing.T, mutate func(*config.Config)) (*Manager, *fake.Driver, *store.Store) {
	t.Helper()
	cfg := config.Default()
	cfg.Network.DefaultMode = "full"
	cfg.Limits.MaxSandboxesPerKey = 10
	if mutate != nil {
		mutate(&cfg)
	}
	st := openStore(t)
	fd := fake.New("docker")
	return New(cfg, st, map[string]driver.Driver{"docker": fd}, discard()), fd, st
}

func TestUpdateNetworkReplacesAllowAndKeepsServerDeny(t *testing.T) {
	mgr, fd, st := netManager(t, func(c *config.Config) { c.Network.EgressDeny = []string{"198.51.100.9"} })
	ctx := context.Background()
	actor := keyActor(t, st, store.KeyNetwork{})
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	up, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Allow: list("203.0.113.0/24", "api.example.com"), Deny: list("203.0.113.5")})
	if err != nil {
		t.Fatal(err)
	}
	if up.Network != "allow" || !sameStrings(up.Allow, []string{"203.0.113.0/24", "api.example.com"}) {
		t.Fatalf("allow not replaced: %+v", up)
	}
	if !sameStrings(up.Deny, []string{"198.51.100.9", "203.0.113.5"}) {
		t.Fatalf("server deny must stay: %v", up.Deny)
	}
	spec, _ := fd.Spec(sb.ID)
	if !sameStrings(spec.Policy.Allow, up.Allow) || !sameStrings(spec.Policy.Deny, up.Deny) {
		t.Fatalf("driver policy = %+v", spec.Policy)
	}
	stored, err := st.GetSandbox(ctx, sb.ID)
	if err != nil || !sameStrings(stored.Allow, up.Allow) || !sameStrings(stored.Deny, up.Deny) {
		t.Fatalf("stored = %+v %v", stored, err)
	}

	// Clearing the sandbox deny list cannot remove the server's.
	up, err = mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Deny: list()})
	if err != nil || !sameStrings(up.Deny, []string{"198.51.100.9"}) {
		t.Fatalf("deny after clear = %+v %v", up.Deny, err)
	}
	// An omitted allow list is kept.
	if !sameStrings(up.Allow, []string{"203.0.113.0/24", "api.example.com"}) {
		t.Fatalf("allow changed without being sent: %v", up.Allow)
	}
}

func TestUpdateNetworkModesAndLimits(t *testing.T) {
	mgr, _, st := netManager(t, func(c *config.Config) { c.Network.DefaultEgressKbps = 2000 })
	ctx := context.Background()
	actor := keyActor(t, st, store.KeyNetwork{})
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	up, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Mode: "full"})
	if err != nil || up.Network != "full" || len(up.Allow) != 0 {
		t.Fatalf("allow -> full should inherit the empty server allow list: %+v %v", up, err)
	}
	if _, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Mode: "allow"}); !isStatus(err, 400) {
		t.Fatalf("allow without a list = %v", err)
	}
	if _, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Mode: "none"}); !isStatus(err, 400) {
		t.Fatalf("full -> none = %v", err)
	}
	if _, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Allow: list("*.example.com")}); !isStatus(err, 400) {
		t.Fatalf("wildcard = %v", err)
	}
	big, small, neg := 5000, 100, -1
	if up, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{EgressKbps: &big}); err != nil || up.EgressKbps != 2000 {
		t.Fatalf("server limit must stay the ceiling: %+v %v", up, err)
	}
	if up, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{EgressKbps: &small, MaxConns: &small}); err != nil || up.EgressKbps != 100 || up.MaxConns != 100 {
		t.Fatalf("tighter limits: %+v %v", up, err)
	}
	if _, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{MaxConns: &neg}); !isStatus(err, 400) {
		t.Fatalf("negative limit = %v", err)
	}

	offline, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UpdateNetwork(ctx, actor, offline.ID, NetworkUpdate{Mode: "full"}); !isStatus(err, 400) {
		t.Fatalf("none -> full = %v", err)
	}
}

func TestUpdateNetworkHonorsOwnerKeyCeiling(t *testing.T) {
	mgr, _, st := netManager(t, nil)
	ctx := context.Background()
	keyList := []string{"example.com", "203.0.113.0/24"}
	owner := keyActor(t, st, store.KeyNetwork{MaxMode: "allow", Allow: &keyList})
	sb, err := mgr.Create(ctx, owner, CreateInput{Image: "alpine:3.20"})
	if err != nil || sb.Network != "allow" {
		t.Fatalf("%+v %v", sb, err)
	}
	if _, err := mgr.UpdateNetwork(ctx, owner, sb.ID, NetworkUpdate{Mode: "full"}); !isStatus(err, 403) {
		t.Fatalf("full over max_mode allow = %v", err)
	}
	if _, err := mgr.UpdateNetwork(ctx, owner, sb.ID, NetworkUpdate{Allow: list("evil.example")}); !isStatus(err, 403) {
		t.Fatalf("allow outside key list = %v", err)
	}
	// An admin acting on the sandbox is still bound by the owner's key.
	if _, err := mgr.UpdateNetwork(ctx, Actor{Admin: true}, sb.ID, NetworkUpdate{Mode: "full"}); !isStatus(err, 403) {
		t.Fatalf("admin full over owner ceiling = %v", err)
	}
	if up, err := mgr.UpdateNetwork(ctx, owner, sb.ID, NetworkUpdate{Allow: list("203.0.113.7")}); err != nil || !sameStrings(up.Allow, []string{"203.0.113.7"}) {
		t.Fatalf("narrowing inside the key list: %+v %v", up, err)
	}

	other := keyActor(t, st, store.KeyNetwork{})
	if _, err := mgr.UpdateNetwork(ctx, other, sb.ID, NetworkUpdate{Allow: list("203.0.113.7")}); !isStatus(err, 404) {
		t.Fatalf("another key's sandbox = %v", err)
	}
}

func TestUpdateNetworkDriverFailureKeepsOldPolicy(t *testing.T) {
	mgr, fd, st := netManager(t, nil)
	ctx := context.Background()
	actor := keyActor(t, st, store.KeyNetwork{})
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	fd.NetErr = errors.New("iptables broke")
	if _, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Allow: list("203.0.113.1")}); !isStatus(err, 502) {
		t.Fatalf("driver error = %v", err)
	}
	stored, err := st.GetSandbox(ctx, sb.ID)
	if err != nil || !sameStrings(stored.Allow, []string{"example.com"}) {
		t.Fatalf("store changed on failure: %+v %v", stored, err)
	}
	fd.NetErr = nil

	if _, err := mgr.Pause(ctx, actor, sb.ID); err != nil {
		t.Fatal(err)
	}
	if up, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Allow: list("203.0.113.1")}); err != nil || up.Status != "paused" {
		t.Fatalf("paused update: %+v %v", up, err)
	}
	if _, err := mgr.Delete(ctx, actor, sb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UpdateNetwork(ctx, actor, sb.ID, NetworkUpdate{Allow: list("203.0.113.1")}); err == nil {
		t.Fatal("update on a deleted sandbox succeeded")
	}
}

func TestUpdateNetworkUnsupportedBackend(t *testing.T) {
	cfg := config.Default()
	cfg.Network.DefaultMode = "full"
	cfg.Backend = "plain"
	st := openStore(t)
	mgr := New(cfg, st, map[string]driver.Driver{"plain": noEnforcer{fake.New("plain")}}, discard())
	ctx := context.Background()
	sb, err := mgr.Create(ctx, Actor{Admin: true}, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UpdateNetwork(ctx, Actor{Admin: true}, sb.ID, NetworkUpdate{Deny: list("203.0.113.1")}); !isStatus(err, 400) {
		t.Fatalf("backend without live updates = %v", err)
	}
}

// noEnforcer hides the fake's NetEnforcer methods, like the Firecracker driver.
type noEnforcer struct{ *fake.Driver }

func (noEnforcer) ApplyNetPolicy() {}
