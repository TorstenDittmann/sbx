package netpolicy

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseAndDecide(t *testing.T) {
	parsed, err := ParseList([]string{"1.1.1.1", "10.0.0.0/8", "*.github.com", "registry.npmjs.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Prefixes()) != 2 || len(parsed.Domains()) != 2 {
		t.Fatalf("%+v", parsed)
	}
	if !MatchDomain("api.github.com", parsed.Entries) || MatchDomain("github.com", parsed.Entries) {
		t.Fatal("wildcard should match only subdomains")
	}
	if !MatchDomain("registry.npmjs.org", parsed.Entries) {
		t.Fatal("exact domain")
	}
	for _, bad := range []string{"http://example.com", "not a host", "", "*.", "ex ample.com"} {
		if _, err := ParseList([]string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}

	base := Baseline{BlockPrivate: true, BlockMetadata: true, PrivateAllow: mustPrefixes("10.1.0.0/16")}
	allow := mustPrefixes("1.1.1.1/32", "10.1.2.3/32")
	dns := mustPrefixes("1.1.1.1/32")
	if d := Decide(netip.MustParseAddr("1.1.1.1"), nil, allow, dns, base); !d.Allow {
		t.Fatal(d)
	}
	if d := Decide(netip.MustParseAddr("8.8.8.8"), nil, allow, dns, base); d.Allow {
		t.Fatal("outside allow list")
	}
	if d := Decide(netip.MustParseAddr("169.254.169.254"), nil, mustPrefixes("169.254.169.254/32"), nil, base); d.Allow || d.Reason != "blocked" {
		t.Fatal(d)
	}
	if d := Decide(netip.MustParseAddr("10.1.2.3"), nil, allow, nil, base); !d.Allow {
		t.Fatalf("private allow exception: %+v", d)
	}
	if d := Decide(netip.MustParseAddr("10.2.2.3"), nil, allow, nil, base); d.Allow {
		t.Fatal("other private should stay blocked")
	}
	if d := Decide(netip.MustParseAddr("8.8.8.8"), mustPrefixes("8.8.8.8/32"), nil, nil, base); d.Allow || d.Reason != "denied" {
		t.Fatal(d)
	}
	open := Baseline{BlockPrivate: false, BlockMetadata: true}
	if Blocked(netip.MustParseAddr("10.9.9.9"), open) {
		t.Fatal("operator opted out of private blocking")
	}
	if !Blocked(netip.MustParseAddr("169.254.169.254"), open) {
		t.Fatal("metadata stays blocked")
	}
}

func TestRenderSandboxAndBaseline(t *testing.T) {
	chain, err := ChainName("sbx_0123456789abcdef")
	if err != nil || chain != "SBX0123456789AB" {
		t.Fatalf("%s %v", chain, err)
	}
	script, err := RenderSandbox(Sandbox{
		Chain: chain, Bridge: "sbx0",
		IPv4: netip.MustParseAddr("172.30.0.8"), IPv6: netip.MustParseAddr("fd16:5b00::8"),
		Deny4: mustPrefixes("9.9.9.9/32"), Allow4: mustPrefixes("1.1.1.1/32"),
		DNS4: mustPrefixes("1.1.1.1/32"), AllowList: true,
		MaxConns: 32, EgressKbps: 256, PID: 42,
		Baseline: Baseline{BlockPrivate: true, BlockMetadata: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SBX-FILTER", "-i sbx0", "-s 172.30.0.8", "ESTABLISHED,RELATED",
		"connlimit-above 32", "-d 9.9.9.9/32", "-d 1.1.1.1/32", "REJECT",
		"ip6tables", "fd16:5b00::8", "tc qdisc replace", "rate 256kbit", "nsenter -t 42",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %s\n%s", want, script)
		}
	}
	base := RenderBaseline("sbx0", Baseline{
		BlockPrivate: true, BlockMetadata: true,
		PrivateAllow: mustPrefixes("10.8.0.0/16"),
	})
	for _, want := range []string{"169.254.0.0/16", "10.0.0.0/8", "10.8.0.0/16", "100.100.100.200/32", "fe80::/10", "fc00::/7", "-j RETURN", "-I DOCKER-USER 1", "-I INPUT 1"} {
		if !strings.Contains(base, want) {
			t.Fatalf("baseline missing %s\n%s", want, base)
		}
	}
	open := RenderBaseline("sbx0", Baseline{BlockPrivate: false, BlockMetadata: true})
	if strings.Contains(open, "10.0.0.0/8") || !strings.Contains(open, "169.254.0.0/16") {
		t.Fatalf("opt-out script\n%s", open)
	}
	drop := RenderDrop(chain, "sbx0", "172.30.0.8", "", "veth123")
	if !strings.Contains(drop, "iptables -w 5 -X "+chain) || !strings.Contains(drop, "tc qdisc del dev veth123") {
		t.Fatal(drop)
	}
	if _, err := RenderSandbox(Sandbox{Chain: chain, Bridge: "sbx0", IPv4: netip.MustParseAddr("172.30.0.8"), EgressKbps: 10}); err == nil {
		t.Fatal("expected pid requirement")
	}
}

func TestRenderBaselineCheckMirrorsInstall(t *testing.T) {
	base := Baseline{BlockPrivate: true, BlockMetadata: true, PrivateAllow: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}}
	install := RenderBaseline("sbx0", base)
	check := RenderBaselineCheck("sbx0", base)
	for _, line := range strings.Split(install, "\n") {
		if rest, ok := strings.CutPrefix(line, "iptables -w 5 -A SBX-EGRESS "); ok {
			if !strings.Contains(check, "iptables -w 5 -C SBX-EGRESS "+rest+"\n") {
				t.Fatalf("check misses %q", rest)
			}
		}
	}
	if !strings.Contains(check, `[ "$(iptables -w 5 -S DOCKER-USER | sed -n 2p)" = "-A DOCKER-USER -i sbx0 -j SBX-EGRESS" ]`) {
		t.Fatal(check)
	}
	for _, op := range []string{" -F ", " -I ", " -D ", " -N "} {
		if strings.Contains(check, op) {
			t.Fatalf("check must not change rules (%s)", op)
		}
	}
}

// A private_allow range that contains a metadata address still opens the rest
// of the range, and metadata stays closed because its reject comes first.
func TestPrivateAllowAroundMetadata(t *testing.T) {
	base := Baseline{BlockPrivate: true, BlockMetadata: true, PrivateAllow: mustPrefixes("100.64.0.0/10")}
	script := RenderBaseline("sbx0", base)
	meta := strings.Index(script, "-A SBX-EGRESS -d 100.100.100.200/32 -j REJECT")
	ret := strings.Index(script, "-A SBX-EGRESS -d 100.64.0.0/10 -j RETURN")
	if meta < 0 || ret < 0 || meta > ret {
		t.Fatalf("want the metadata reject before the private_allow return\n%s", script)
	}
	if Blocked(netip.MustParseAddr("100.64.0.1"), base) || !Blocked(netip.MustParseAddr("100.100.100.200"), base) {
		t.Fatal("Blocked disagrees with the rendered rules")
	}
}
