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
