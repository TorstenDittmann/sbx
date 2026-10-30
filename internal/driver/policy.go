package driver

import (
	"errors"
	"net/netip"
)

// ErrConfig means the sandbox spec cannot be honored by this backend.
// Callers map it to HTTP 400.
var ErrConfig = errors.New("invalid sandbox config")

// BlockedIPv4 are destinations a sandbox must not reach even on network=full.
// Link-local covers the metadata address used by AWS, GCP, Azure, and others.
// 100.100.100.200 is the Alibaba Cloud metadata service.
var BlockedIPv4 = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"0.0.0.0/8",
	"127.0.0.0/8",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"169.254.0.0/16",
	"100.100.100.200/32",
}

// BlockedIPv6 covers link-local, unique-local, and loopback.
var BlockedIPv6 = []string{
	"fe80::/10",
	"fc00::/7",
	"::1/128",
}

// Blocked reports whether ip is a destination a sandbox must not reach.
func Blocked(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	for _, cidr := range append(append([]string{}, BlockedIPv4...), BlockedIPv6...) {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return true
		}
	}
	return false
}
