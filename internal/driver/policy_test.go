package driver

import (
	"net/netip"
	"testing"
)

func TestBlockedRanges(t *testing.T) {
	for _, ip := range []string{"169.254.169.254", "10.1.2.3", "192.168.1.1", "172.16.0.1", "100.100.100.200", "::1", "fe80::1", "fd00::1"} {
		if !Blocked(netip.MustParseAddr(ip)) {
			t.Fatalf("%s should be blocked", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2001:4860:4860::8888"} {
		if Blocked(netip.MustParseAddr(ip)) {
			t.Fatalf("%s should be allowed", ip)
		}
	}
	if !Blocked(netip.Addr{}) {
		t.Fatal("invalid address")
	}
}
