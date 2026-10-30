package firecracker

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"strings"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/netpolicy"
)

var tapNameRE = regexp.MustCompile(`^[a-z0-9]{1,15}$`)

// fallbackDNS is how a guest with an allow list still resolves names.
var fallbackDNS = []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}

// NetSpec is one sandbox NIC and the host rules that confine it.
type NetSpec struct {
	Tap          string
	HostCIDR     string
	GuestIP      string
	GuestCIDR    string
	Gateway      string
	Mode         driver.NetMode
	Allow        []string
	Deny         []string
	Metadata     []string
	Blocked      []string
	PrivateAllow []string
	AllowList    bool
	MaxConns     int
	Script       string
	Index        int
}

func chainName(tap string) string { return "SBX" + tap }

// inputChainName filters what the guest sends to the host itself. FORWARD
// never sees those packets, so without it the guest reaches every service
// listening on the host through the tap gateway.
func inputChainName(tap string) string { return "SBXI" + tap }

func netScript(spec NetSpec) (string, error) {
	if !tapNameRE.MatchString(spec.Tap) {
		return "", fmt.Errorf("tap name %q", spec.Tap)
	}
	chain := chainName(spec.Tap)
	var b strings.Builder
	b.WriteString("set -e\n")
	fmt.Fprintf(&b, "ip tuntap add dev %s mode tap 2>/dev/null || true\n", spec.Tap)
	fmt.Fprintf(&b, "ip addr replace %s dev %s\n", spec.HostCIDR, spec.Tap)
	fmt.Fprintf(&b, "ip link set %s up\n", spec.Tap)
	b.WriteString("sysctl -w net.ipv4.ip_forward=1 >/dev/null\n")
	fmt.Fprintf(&b, "iptables -w 5 -N %s 2>/dev/null || true\n", chain)
	fmt.Fprintf(&b, "iptables -w 5 -F %s\n", chain)
	fmt.Fprintf(&b, "iptables -w 5 -A %s -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN\n", chain)
	if spec.MaxConns > 0 {
		fmt.Fprintf(&b, "iptables -w 5 -A %s -m connlimit --connlimit-above %d --connlimit-mask 32 --connlimit-saddr -j REJECT --reject-with icmp-host-prohibited\n", chain, spec.MaxConns)
	}
	// Metadata first, so a private_allow range that contains a metadata
	// address cannot open it.
	for _, cidr := range spec.Metadata {
		fmt.Fprintf(&b, "iptables -w 5 -A %s -d %s -j REJECT --reject-with icmp-host-prohibited\n", chain, cidr)
	}
	for _, cidr := range spec.PrivateAllow {
		fmt.Fprintf(&b, "iptables -w 5 -A %s -d %s -j RETURN\n", chain, cidr)
	}
	for _, cidr := range spec.Deny {
		fmt.Fprintf(&b, "iptables -w 5 -A %s -d %s -j REJECT --reject-with icmp-host-prohibited\n", chain, cidr)
	}
	for _, cidr := range spec.Blocked {
		fmt.Fprintf(&b, "iptables -w 5 -A %s -d %s -j REJECT --reject-with icmp-host-prohibited\n", chain, cidr)
	}
	if spec.AllowList {
		for _, cidr := range spec.Allow {
			fmt.Fprintf(&b, "iptables -w 5 -A %s -d %s -j ACCEPT\n", chain, cidr)
		}
		fmt.Fprintf(&b, "iptables -w 5 -A %s -j REJECT --reject-with icmp-host-prohibited\n", chain)
	} else {
		fmt.Fprintf(&b, "iptables -w 5 -A %s -j RETURN\n", chain)
	}
	fmt.Fprintf(&b, "iptables -w 5 -C FORWARD -i %s -j %s 2>/dev/null || iptables -w 5 -I FORWARD 1 -i %s -j %s\n", spec.Tap, chain, spec.Tap, chain)
	fmt.Fprintf(&b, "iptables -t nat -w 5 -C POSTROUTING -s %s/32 -j MASQUERADE 2>/dev/null || iptables -t nat -w 5 -A POSTROUTING -s %s/32 -j MASQUERADE\n", spec.GuestIP, spec.GuestIP)
	in := inputChainName(spec.Tap)
	fmt.Fprintf(&b, "iptables -w 5 -N %s 2>/dev/null || true\n", in)
	fmt.Fprintf(&b, "iptables -w 5 -F %s\n", in)
	fmt.Fprintf(&b, "iptables -w 5 -A %s -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN\n", in)
	fmt.Fprintf(&b, "iptables -w 5 -A %s -j REJECT --reject-with icmp-host-prohibited\n", in)
	fmt.Fprintf(&b, "iptables -w 5 -C INPUT -i %s -j %s 2>/dev/null || iptables -w 5 -I INPUT 1 -i %s -j %s\n", spec.Tap, in, spec.Tap, in)
	return b.String(), nil
}

func teardownScript(spec NetSpec) string {
	chain := chainName(spec.Tap)
	var b strings.Builder
	fmt.Fprintf(&b, "iptables -w 5 -D FORWARD -i %s -j %s 2>/dev/null || true\n", spec.Tap, chain)
	fmt.Fprintf(&b, "iptables -w 5 -F %s 2>/dev/null || true\n", chain)
	fmt.Fprintf(&b, "iptables -w 5 -X %s 2>/dev/null || true\n", chain)
	in := inputChainName(spec.Tap)
	fmt.Fprintf(&b, "iptables -w 5 -D INPUT -i %s -j %s 2>/dev/null || true\n", spec.Tap, in)
	fmt.Fprintf(&b, "iptables -w 5 -F %s 2>/dev/null || true\n", in)
	fmt.Fprintf(&b, "iptables -w 5 -X %s 2>/dev/null || true\n", in)
	fmt.Fprintf(&b, "iptables -t nat -w 5 -D POSTROUTING -s %s/32 -j MASQUERADE 2>/dev/null || true\n", spec.GuestIP)
	fmt.Fprintf(&b, "ip link del %s 2>/dev/null || true\n", spec.Tap)
	return b.String()
}

func runScript(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func resolveAllow(ctx context.Context, hosts []string, policy driver.NetPolicy, rejectBlocked bool, lookup func(context.Context, string) ([]netip.Addr, error)) ([]string, error) {
	parsed, err := netpolicy.ParseList(hosts)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", driver.ErrConfig, err.Error())
	}
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	seen := map[string]bool{}
	var out []string
	blocked := blockedPrefixes(policy)
	add := func(p netip.Prefix) error {
		if !p.IsValid() {
			return fmt.Errorf("%w: invalid prefix", driver.ErrConfig)
		}
		p = p.Masked()
		if rejectBlocked {
			for _, cidr := range blocked {
				if p.Overlaps(netip.MustParsePrefix(cidr)) {
					return fmt.Errorf("%w: %s overlaps a blocked range", driver.ErrConfig, p)
				}
			}
		}
		if !seen[p.String()] {
			seen[p.String()] = true
			out = append(out, p.String())
		}
		return nil
	}
	for _, entry := range parsed.Entries {
		if entry.IsNet {
			if err := add(entry.Prefix); err != nil {
				return nil, err
			}
			continue
		}
		addrs, err := lookup(ctx, entry.Domain)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", driver.ErrConfig, entry.Domain, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("%w: %s did not resolve", driver.ErrConfig, entry.Domain)
		}
		for _, ip := range addrs {
			bits := 32
			if ip.Is6() {
				bits = 128
			}
			if err := add(netip.PrefixFrom(ip.Unmap(), bits)); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: allow list is empty", driver.ErrConfig)
	}
	return out, nil
}

func blockedPrefixes(policy driver.NetPolicy) []string {
	return append(metadataPrefixes(policy), privatePrefixes(policy)...)
}

func metadataPrefixes(policy driver.NetPolicy) []string {
	if !policy.BlockMetadata {
		return nil
	}
	return []string{"169.254.0.0/16", "100.100.100.200/32", "fe80::/10", "::1/128"}
}

func privatePrefixes(policy driver.NetPolicy) []string {
	if !policy.BlockPrivate {
		return nil
	}
	var out []string
	v4, v6 := netpolicy.PrivatePrefixes()
	for _, p := range append(v4, v6...) {
		out = append(out, p.String())
	}
	return out
}

func ipv4Only(cidrs []string) []string {
	var out []string
	for _, cidr := range cidrs {
		p, err := netip.ParsePrefix(cidr)
		if err != nil || !p.Addr().Is4() {
			continue
		}
		out = append(out, p.String())
	}
	return out
}

func privateAllowV4(raw []string) []string {
	var out []string
	for _, item := range raw {
		p, err := netip.ParsePrefix(item)
		if err != nil {
			addr, err2 := netip.ParseAddr(item)
			if err2 != nil || !addr.Is4() {
				continue
			}
			p = netip.PrefixFrom(addr, 32)
		}
		if !p.Addr().Is4() {
			continue
		}
		out = append(out, p.Masked().String())
	}
	return out
}

func subnet(n int) (hostCIDR, guestCIDR, guestIP, gateway string, err error) {
	if n < 1 || n > 16000 {
		return "", "", "", "", fmt.Errorf("subnet index %d", n)
	}
	offset := (n - 1) * 4
	third := offset / 256
	fourth := offset % 256
	gateway = fmt.Sprintf("172.31.%d.%d", third, fourth+1)
	guestIP = fmt.Sprintf("172.31.%d.%d", third, fourth+2)
	hostCIDR = gateway + "/30"
	guestCIDR = guestIP + "/30"
	return hostCIDR, guestCIDR, guestIP, gateway, nil
}
