package docker

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestCompileListsResolvesDomains(t *testing.T) {
	prev := lookupHost
	lookupHost = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("2606:2800:220:1:248:1893:25c8:1946")}, nil
	}
	t.Cleanup(func() { lookupHost = prev })

	allow4, allow6, _, _, dns4, dns6, allowList, err := compileLists(context.Background(), driver.NetPolicy{
		Allow: []string{"example.com", "1.1.1.1/32"},
		DNS:   []string{"1.1.1.1", "2606:4700:4700::1111"},
	})
	if err != nil || !allowList {
		t.Fatal(err, allowList)
	}
	if len(allow4) != 2 || len(allow6) != 1 || len(dns4) != 1 || len(dns6) != 1 {
		t.Fatalf("v4 %v v6 %v dns4 %v dns6 %v", allow4, allow6, dns4, dns6)
	}
	_, _, _, _, _, _, _, err = compileLists(context.Background(), driver.NetPolicy{Allow: []string{"*.example.com"}})
	if err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatal(err)
	}
	_, _, _, _, _, _, _, err = compileLists(context.Background(), driver.NetPolicy{DNS: []string{"dns.example"}})
	if err == nil || !strings.Contains(err.Error(), "dns resolver") {
		t.Fatal(err)
	}
}

func newPolicyDriver(t *testing.T, run func(string)) (*Driver, *fakeDaemon) {
	t.Helper()
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{
		Name: "docker", UID: 1000, GID: 1000,
		ScriptRunner: func(_ context.Context, script string) error {
			if run != nil {
				run(script)
			}
			return nil
		},
	}, cli)
	return d, fd
}

func addRunning(fd *fakeDaemon, id string) {
	fd.mu.Lock()
	fd.containers[id] = &fakeContainer{id: id, status: "running"}
	fd.mu.Unlock()
}

func tracked(d *Driver, id string) bool {
	d.polMu.Lock()
	defer d.polMu.Unlock()
	_, ok := d.policies[id]
	return ok
}

const policyChain = "SBX0123456789AB"

func isDrop(script string) bool {
	return strings.HasPrefix(script, "set +e") && strings.Contains(script, "-X "+policyChain)
}

func isInstall(script string) bool {
	return strings.HasPrefix(script, "set -e") && strings.Contains(script, policyChain)
}

func TestRefreshForgetsRemovedContainers(t *testing.T) {
	var drops int
	d, fd := newPolicyDriver(t, func(script string) {
		if isDrop(script) {
			drops++
		}
	})
	ctx := context.Background()
	spec := driver.SandboxSpec{ID: "sbx_0123456789abcdef", Network: driver.NetFull}
	addRunning(fd, "cid-gone")
	if err := d.ApplyNetPolicy(ctx, "cid-gone", spec); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	delete(fd.containers, "cid-gone")
	fd.mu.Unlock()
	if err := d.RefreshNetPolicies(ctx); err != nil {
		t.Fatalf("refresh with a removed container = %v", err)
	}
	if tracked(d, "cid-gone") || drops != 1 {
		t.Fatalf("removed container: tracked=%v drops=%d", tracked(d, "cid-gone"), drops)
	}
	if err := d.RefreshNetPolicies(ctx); err != nil {
		t.Fatalf("second refresh = %v", err)
	}
}

func TestRefreshDoesNotResurrectDeletedPolicy(t *testing.T) {
	var d *Driver
	var refreshing, deleted bool
	var last string
	d, fd := newPolicyDriver(t, func(script string) {
		last = script
		if refreshing && !deleted && isInstall(script) {
			// Delete lands while the refresh is reinstalling this sandbox.
			deleted = true
			if err := d.Delete(context.Background(), "cid-race"); err != nil {
				t.Error(err)
			}
		}
	})
	ctx := context.Background()
	spec := driver.SandboxSpec{ID: "sbx_0123456789abcdef", Network: driver.NetFull}
	addRunning(fd, "cid-race")
	if err := d.ApplyNetPolicy(ctx, "cid-race", spec); err != nil {
		t.Fatal(err)
	}
	refreshing = true
	if err := d.RefreshNetPolicies(ctx); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("delete did not run during the refresh")
	}
	if tracked(d, "cid-race") {
		t.Fatal("refresh brought back a deleted sandbox's policy")
	}
	if !isDrop(last) {
		t.Fatalf("rules installed by the refresh were not dropped:\n%s", last)
	}
	if err := d.RefreshNetPolicies(ctx); err != nil {
		t.Fatalf("next refresh = %v", err)
	}
}
