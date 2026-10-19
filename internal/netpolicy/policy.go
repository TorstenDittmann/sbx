// Package netpolicy turns egress allow and deny lists into decisions and
// iptables scripts. Domain names are resolved by the caller; this package only
// sees addresses, prefixes, and a script.
package netpolicy

import (
	"fmt"
	"net/netip"
	"strings"
)

// Baseline is the operator-wide block list. It is not overridable per sandbox.
type Baseline struct {
	BlockPrivate  bool
	BlockMetadata bool
	// PrivateAllow lists CIDRs that skip the private-range block. Metadata
	// and link-local addresses stay blocked while BlockMetadata is set.
	PrivateAllow []netip.Prefix
}

// Entry is one allow or deny item before DNS resolution.
type Entry struct {
	Prefix   netip.Prefix
	Domain   string
	Wildcard bool
	IsNet    bool
}

// Parsed is a validated allow or deny list.
type Parsed struct {
	Entries []Entry
}

// ParseList validates domains, IPs, and CIDRs. Wildcards are a leading "*.".
func ParseList(items []string) (Parsed, error) {
	if len(items) > 64 {
		return Parsed{}, fmt.Errorf("too many network rules")
	}
	var out Parsed
	seen := map[string]struct{}{}
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			return Parsed{}, fmt.Errorf("empty network rule")
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		entry, err := parseEntry(item)
		if err != nil {
			return Parsed{}, err
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}

func parseEntry(item string) (Entry, error) {
	if strings.Contains(item, "://") || strings.ContainsAny(item, " \t") {
		return Entry{}, fmt.Errorf("network rule %q must be a domain, IP, or CIDR", item)
	}
	if addr, err := netip.ParseAddr(item); err == nil {
		bits := 32
		if addr.Is6() {
			bits = 128
		}
		return Entry{Prefix: netip.PrefixFrom(addr, bits), IsNet: true}, nil
	}
	if prefix, err := netip.ParsePrefix(item); err == nil {
		return Entry{Prefix: prefix.Masked(), IsNet: true}, nil
	}
	wild := false
	name := item
	if strings.HasPrefix(name, "*.") {
		wild = true
		name = strings.TrimPrefix(name, "*.")
	}
	if !validDomain(name) {
		return Entry{}, fmt.Errorf("network rule %q is not a domain, IP, or CIDR", item)
	}
	return Entry{Domain: strings.ToLower(name), Wildcard: wild}, nil
}

func validDomain(name string) bool {
	if name == "" || len(name) > 253 || strings.Contains(name, "..") {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			if !ok {
				return false
			}
		}
	}
	return true
}

// Domains returns names that need DNS, without the wildcard marker.
func (p Parsed) Domains() []string {
	var out []string
	for _, e := range p.Entries {
		if e.Domain != "" {
			out = append(out, e.Domain)
		}
	}
	return out
}

// Prefixes returns CIDR and IP entries.
func (p Parsed) Prefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, e := range p.Entries {
		if e.IsNet {
			out = append(out, e.Prefix)
		}
	}
	return out
}

// MatchDomain reports whether host is covered by a domain entry.
// A wildcard matches only subdomains, not the apex.
func MatchDomain(host string, entries []Entry) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, e := range entries {
		if e.Domain == "" {
			continue
		}
		if e.Wildcard {
			if host != e.Domain && strings.HasSuffix(host, "."+e.Domain) {
				return true
			}
			continue
		}
		if host == e.Domain {
			return true
		}
	}
	return false
}

// Contains reports whether ip is inside any prefix.
func Contains(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// MetadataPrefixes are cloud metadata and link-local ranges.
func MetadataPrefixes() (v4, v6 []netip.Prefix) {
	v4 = mustPrefixes("169.254.0.0/16", "100.100.100.200/32")
	v6 = mustPrefixes("fe80::/10", "::1/128")
	return v4, v6
}

// PrivatePrefixes are RFC1918 and IPv6 unique-local space.
func PrivatePrefixes() (v4, v6 []netip.Prefix) {
	v4 = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
	v6 = mustPrefixes("fc00::/7")
	return v4, v6
}

func overlapsMetadata(p netip.Prefix) bool {
	meta4, meta6 := MetadataPrefixes()
	metas := meta4
	if p.Addr().Is6() {
		metas = meta6
	}
	for _, m := range metas {
		if m.Overlaps(p) {
			return true
		}
	}
	return false
}

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// Blocked reports whether ip is rejected by the operator baseline.
// An address in PrivateAllow is not blocked as private. Metadata still is,
// unless BlockMetadata is false.
func Blocked(ip netip.Addr, base Baseline) bool {
	meta4, meta6 := MetadataPrefixes()
	if base.BlockMetadata && (Contains(ip, meta4) || Contains(ip, meta6)) {
		return true
	}
	if !base.BlockPrivate || Contains(ip, base.PrivateAllow) {
		return false
	}
	priv4, priv6 := PrivatePrefixes()
	return Contains(ip, priv4) || Contains(ip, priv6)
}

// Decision is the per-sandbox verdict for one destination address.
type Decision struct {
	Allow  bool
	Reason string
}

// Decide applies deny, then the operator baseline, then an optional allow list.
// dns lists resolver addresses that stay reachable when an allow list is set.
// An empty allow list means public egress is otherwise open.
func Decide(ip netip.Addr, deny, allow, dns []netip.Prefix, base Baseline) Decision {
	if Contains(ip, deny) {
		return Decision{Reason: "denied"}
	}
	if Blocked(ip, base) {
		return Decision{Reason: "blocked"}
	}
	if len(allow) == 0 {
		return Decision{Allow: true, Reason: "full"}
	}
	if Contains(ip, dns) || Contains(ip, allow) {
		return Decision{Allow: true, Reason: "allowed"}
	}
	return Decision{Reason: "not in allow list"}
}

// Sandbox is the input to a per-sandbox filter script.
type Sandbox struct {
	Chain      string
	Bridge     string
	IPv4       netip.Addr
	IPv6       netip.Addr
	Deny4      []netip.Prefix
	Deny6      []netip.Prefix
	Allow4     []netip.Prefix
	Allow6     []netip.Prefix
	DNS4       []netip.Prefix
	DNS6       []netip.Prefix
	AllowList  bool
	MaxConns   int
	EgressKbps int
	PID        int
	Baseline   Baseline
}

// ChainName is the iptables chain for a sandbox id. Ids look like sbx_<hex>.
func ChainName(sandboxID string) (string, error) {
	hex := strings.TrimPrefix(sandboxID, "sbx_")
	hex = strings.ToUpper(hex)
	if len(hex) > 12 {
		hex = hex[:12]
	}
	name := "SBX" + hex
	for _, c := range name {
		ok := (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return "", fmt.Errorf("sandbox id %q cannot be an iptables chain", sandboxID)
		}
	}
	if len(name) < 4 || len(name) > 28 {
		return "", fmt.Errorf("sandbox id %q cannot be an iptables chain", sandboxID)
	}
	return name, nil
}

// RenderSandbox writes an idempotent shell script. Prefixes and the chain
// name must already be validated; nothing else is interpolated.
func RenderSandbox(s Sandbox) (string, error) {
	if s.Chain == "" || s.Bridge == "" || !s.IPv4.Is4() {
		return "", fmt.Errorf("sandbox filter needs a chain, bridge, and ipv4 address")
	}
	if s.MaxConns < 0 || s.EgressKbps < 0 {
		return "", fmt.Errorf("limits must be >= 0")
	}
	if s.EgressKbps > 0 && s.PID < 2 {
		return "", fmt.Errorf("bandwidth limit needs the container pid")
	}
	var b strings.Builder
	b.WriteString("set -e\n")
	writeFamily(&b, "iptables", "icmp-host-prohibited", s, s.IPv4, s.Deny4, s.Allow4, s.DNS4, false)
	if s.IPv6.Is6() {
		b.WriteString("if command -v ip6tables >/dev/null 2>&1 && ip6tables -w 5 -L DOCKER-USER -n >/dev/null 2>&1; then\n")
		writeFamily(&b, "ip6tables", "icmp6-adm-prohibited", s, s.IPv6, s.Deny6, s.Allow6, s.DNS6, true)
		b.WriteString("fi\n")
	}
	if s.EgressKbps > 0 {
		fmt.Fprintf(&b, "command -v tc >/dev/null 2>&1\n")
		fmt.Fprintf(&b, "command -v nsenter >/dev/null 2>&1\n")
		fmt.Fprintf(&b, "ifindex=$(nsenter -t %d -n cat /sys/class/net/eth0/iflink)\n", s.PID)
		b.WriteString("dev=\n")
		b.WriteString("for path in /sys/class/net/*; do\n")
		b.WriteString("  if [ \"$(cat \"$path/ifindex\" 2>/dev/null || true)\" = \"$ifindex\" ]; then dev=$(basename \"$path\"); break; fi\n")
		b.WriteString("done\n")
		b.WriteString("test -n \"$dev\"\n")
		fmt.Fprintf(&b, "tc qdisc replace dev \"$dev\" root tbf rate %dkbit burst 32kb latency 400ms\n", s.EgressKbps)
	}
	return b.String(), nil
}

func writeFamily(b *strings.Builder, bin, reject string, s Sandbox, src netip.Addr, deny, allow, dns []netip.Prefix, v6 bool) {
	fmt.Fprintf(b, "%s -w 5 -N SBX-FILTER 2>/dev/null || true\n", bin)
	fmt.Fprintf(b, "%s -w 5 -C DOCKER-USER -i %s -j SBX-FILTER 2>/dev/null || %s -w 5 -I DOCKER-USER 1 -i %s -j SBX-FILTER\n", bin, s.Bridge, bin, s.Bridge)
	fmt.Fprintf(b, "%s -w 5 -C INPUT -i %s -j SBX-FILTER 2>/dev/null || %s -w 5 -I INPUT 1 -i %s -j SBX-FILTER\n", bin, s.Bridge, bin, s.Bridge)
	fmt.Fprintf(b, "%s -w 5 -N %s 2>/dev/null || true\n", bin, s.Chain)
	fmt.Fprintf(b, "%s -w 5 -F %s\n", bin, s.Chain)
	fmt.Fprintf(b, "while %s -w 5 -D SBX-FILTER -s %s -j %s 2>/dev/null; do :; done\n", bin, src.String(), s.Chain)
	fmt.Fprintf(b, "%s -w 5 -I SBX-FILTER 1 -s %s -j %s\n", bin, src.String(), s.Chain)
	fmt.Fprintf(b, "%s -w 5 -A %s -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN\n", bin, s.Chain)
	if s.MaxConns > 0 {
		fmt.Fprintf(b, "%s -w 5 -A %s -m connlimit --connlimit-above %d --connlimit-mask %d --connlimit-saddr -j REJECT --reject-with %s\n",
			bin, s.Chain, s.MaxConns, maskBits(v6), reject)
	}
	for _, p := range deny {
		fmt.Fprintf(b, "%s -w 5 -A %s -d %s -j REJECT --reject-with %s\n", bin, s.Chain, p.String(), reject)
	}
	if s.AllowList {
		for _, p := range dns {
			fmt.Fprintf(b, "%s -w 5 -A %s -d %s -j RETURN\n", bin, s.Chain, p.String())
		}
		for _, p := range allow {
			fmt.Fprintf(b, "%s -w 5 -A %s -d %s -j RETURN\n", bin, s.Chain, p.String())
		}
		fmt.Fprintf(b, "%s -w 5 -A %s -j REJECT --reject-with %s\n", bin, s.Chain, reject)
		return
	}
	fmt.Fprintf(b, "%s -w 5 -A %s -j RETURN\n", bin, s.Chain)
}

func maskBits(v6 bool) int {
	if v6 {
		return 128
	}
	return 32
}

// RenderDrop removes one sandbox chain. hostDev is optional.
func RenderDrop(chain, bridge, ipv4, ipv6, hostDev string) string {
	var b strings.Builder
	b.WriteString("set +e\n")
	if ipv4 != "" {
		fmt.Fprintf(&b, "while iptables -w 5 -D SBX-FILTER -s %s -j %s 2>/dev/null; do :; done\n", ipv4, chain)
		fmt.Fprintf(&b, "iptables -w 5 -F %s\n", chain)
		fmt.Fprintf(&b, "iptables -w 5 -X %s\n", chain)
	}
	if ipv6 != "" {
		fmt.Fprintf(&b, "while ip6tables -w 5 -D SBX-FILTER -s %s -j %s 2>/dev/null; do :; done\n", ipv6, chain)
		fmt.Fprintf(&b, "ip6tables -w 5 -F %s\n", chain)
		fmt.Fprintf(&b, "ip6tables -w 5 -X %s\n", chain)
	}
	if hostDev != "" {
		fmt.Fprintf(&b, "tc qdisc del dev %s root\n", hostDev)
	}
	_ = bridge
	return b.String()
}

// RenderBaseline writes the bridge-wide metadata and private-range chain.
// Per-sandbox chains run first and cannot open these ranges, except CIDRs
// listed in PrivateAllow, which return before the private rejects.
func RenderBaseline(bridge string, base Baseline) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	writeBaselineFamily(&b, "iptables", bridge, "icmp-host-prohibited", base, false)
	b.WriteString("if command -v ip6tables >/dev/null 2>&1 && ip6tables -w 5 -L DOCKER-USER -n >/dev/null 2>&1; then\n")
	writeBaselineFamily(&b, "ip6tables", bridge, "icmp6-adm-prohibited", base, true)
	b.WriteString("fi\n")
	return b.String()
}

func writeBaselineFamily(b *strings.Builder, bin, bridge, reject string, base Baseline, v6 bool) {
	fmt.Fprintf(b, "%s -w 5 -N SBX-EGRESS 2>/dev/null || true\n", bin)
	fmt.Fprintf(b, "%s -w 5 -F SBX-EGRESS\n", bin)
	for _, p := range base.PrivateAllow {
		if p.Addr().Is6() != v6 || overlapsMetadata(p) {
			continue
		}
		fmt.Fprintf(b, "%s -w 5 -A SBX-EGRESS -d %s -j RETURN\n", bin, p.String())
	}
	var prefixes []netip.Prefix
	if base.BlockMetadata {
		v4, v6p := MetadataPrefixes()
		if v6 {
			prefixes = append(prefixes, v6p...)
		} else {
			prefixes = append(prefixes, v4...)
		}
	}
	if base.BlockPrivate {
		v4, v6p := PrivatePrefixes()
		if v6 {
			prefixes = append(prefixes, v6p...)
		} else {
			prefixes = append(prefixes, v4...)
		}
	}
	for _, p := range prefixes {
		fmt.Fprintf(b, "%s -w 5 -A SBX-EGRESS -d %s -j REJECT --reject-with %s\n", bin, p.String(), reject)
	}
	fmt.Fprintf(b, "%s -w 5 -A SBX-EGRESS -j RETURN\n", bin)
	fmt.Fprintf(b, "%s -w 5 -C DOCKER-USER -i %s -j SBX-EGRESS 2>/dev/null || %s -w 5 -A DOCKER-USER -i %s -j SBX-EGRESS\n", bin, bridge, bin, bridge)
	fmt.Fprintf(b, "%s -w 5 -C INPUT -i %s -j SBX-EGRESS 2>/dev/null || %s -w 5 -A INPUT -i %s -j SBX-EGRESS\n", bin, bridge, bin, bridge)
}
