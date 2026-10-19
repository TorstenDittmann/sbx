package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func TestNetworkPolicyInheritance(t *testing.T) {
	cfg := config.Default()
	cfg.Network.DefaultMode = "none"
	cfg.Network.EgressDeny = []string{"example.org"}
	cfg.Network.DefaultEgressKbps = 8000
	cfg.Network.DefaultMaxConns = 40
	cfg.Network.PreviewMaxStreams = 1
	st := openStore(t)
	fd := fake.New("docker")
	mgr := New(cfg, st, map[string]driver.Driver{"docker": fd}, discard())
	ctx := context.Background()

	allow := []string{"1.1.1.1/32"}
	deny := []string{"9.9.9.9/32"}
	key := store.APIKey{
		ID: id.RecordID(), Name: "net", Hash: strings.Repeat("ab", 32), Prefix: "sbx_netkey00",
		Scopes:    []string{"sandbox"},
		Network:   store.KeyNetwork{Mode: "full", Allow: &allow, Deny: &deny, EgressKbps: 2000, MaxConns: 10},
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	actor := Actor{KeyID: key.ID}
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	if sb.Network != "full" || len(sb.Allow) != 1 || sb.Allow[0] != "1.1.1.1/32" {
		t.Fatalf("key policy %+v", sb)
	}
	if !contains(sb.Deny, "example.org") || !contains(sb.Deny, "9.9.9.9/32") || sb.EgressKbps != 2000 || sb.MaxConns != 10 {
		t.Fatalf("union %+v", sb)
	}

	kbps, conns := 500, 4
	over, err := mgr.Create(ctx, actor, CreateInput{
		Image: "alpine:3.20", Network: "allow",
		HasAllow: true, Allow: []string{"example.com"},
		HasDeny: true, Deny: []string{"1.0.0.1/32"},
		EgressKbps: &kbps, MaxConns: &conns,
	})
	if err != nil {
		t.Fatal(err)
	}
	if over.Network != "allow" || len(over.Allow) != 1 || over.Allow[0] != "example.com" {
		t.Fatalf("override %+v", over)
	}
	if !contains(over.Deny, "example.org") || !contains(over.Deny, "1.0.0.1/32") || over.EgressKbps != 500 || over.MaxConns != 4 {
		t.Fatalf("tightest %+v", over)
	}

	empty := []string{}
	cleared, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "full", HasAllow: true, Allow: empty})
	if err != nil || len(cleared.Allow) != 0 || cleared.Network != "full" {
		t.Fatalf("empty allow %+v %v", cleared, err)
	}
	offline, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "none", HasAllow: true, Allow: []string{"1.1.1.1"}})
	if err != nil || offline.Network != "none" || len(offline.Allow) != 0 || offline.EgressKbps != 0 {
		t.Fatalf("none %+v %v", offline, err)
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "allow", HasAllow: true, Allow: []string{}}); err == nil {
		t.Fatal("allow without a list")
	}
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", Network: "full", HasAllow: true, Allow: []string{"*.example.com"}}); err == nil {
		t.Fatal("wildcard")
	}
	neg := -1
	if _, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20", EgressKbps: &neg}); err == nil {
		t.Fatal("negative bandwidth")
	}

	pv, err := mgr.CreatePreview(ctx, actor, sb.ID, 8080, false)
	if err != nil || pv.Token == "" || !strings.HasPrefix(pv.Token, "pvt_") {
		t.Fatalf("token %+v %v", pv, err)
	}
	if err := mgr.AcquirePreview(sb.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AcquirePreview(sb.ID); err == nil {
		t.Fatal("expected stream limit")
	}
	mgr.ReleasePreview(sb.ID)
	if err := mgr.AcquirePreview(sb.ID); err != nil {
		t.Fatal(err)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
