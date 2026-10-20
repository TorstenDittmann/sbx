package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/netpolicy"
)

// lookupHost resolves a domain. Tests replace it.
var lookupHost = func(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	return addrs, nil
}

// ApplyNetPolicy installs per-sandbox egress rules. It is safe to call again
// after a restart or when a domain's addresses change.
func (d *Driver) ApplyNetPolicy(ctx context.Context, containerID string, spec driver.SandboxSpec) error {
	if d.opt.NoEgressFilter || spec.Network != driver.NetFull {
		return nil
	}
	if err := d.ensureEgress(ctx); err != nil {
		return err
	}
	st, err := d.waitAddress(ctx, containerID)
	if err != nil {
		return err
	}
	chain, err := netpolicy.ChainName(spec.ID)
	if err != nil {
		return err
	}
	rendered, err := d.renderPolicy(ctx, chain, st, spec)
	if err != nil {
		return err
	}
	if err := d.installScript(ctx, rendered); err != nil {
		return fmt.Errorf("egress policy: %w", err)
	}
	d.polMu.Lock()
	d.policies[containerID] = appliedNet{spec: spec, ipv4: st.IPv4, ipv6: st.IPv6, chain: chain}
	d.polMu.Unlock()
	return nil
}

// DropNetPolicy removes the per-sandbox chain. Missing rules are ignored.
func (d *Driver) DropNetPolicy(ctx context.Context, containerID string) error {
	d.dropNetPolicy(ctx, containerID)
	return nil
}

// RefreshNetPolicies re-resolves domains and reinstalls rules for sandboxes
// this process has applied.
func (d *Driver) RefreshNetPolicies(ctx context.Context) error {
	reinstalled, baseErr := d.verifyEgress(ctx)
	if reinstalled && baseErr == nil {
		baseErr = errors.New("egress baseline rules were missing and have been reinstalled")
	}
	d.polMu.Lock()
	snap := make([]appliedNet, 0, len(d.policies))
	ids := make([]string, 0, len(d.policies))
	for id, pol := range d.policies {
		snap = append(snap, pol)
		ids = append(ids, id)
	}
	d.polMu.Unlock()
	var first error
	for i, pol := range snap {
		if err := d.ApplyNetPolicy(ctx, ids[i], pol.spec); err != nil && first == nil {
			first = err
		}
	}
	if baseErr != nil {
		return baseErr
	}
	return first
}

func (d *Driver) dropNetPolicy(ctx context.Context, containerID string) {
	d.polMu.Lock()
	pol, ok := d.policies[containerID]
	delete(d.policies, containerID)
	d.polMu.Unlock()
	if !ok || d.opt.NoEgressFilter {
		return
	}
	script := netpolicy.RenderDrop(pol.chain, d.bridgeName(), pol.ipv4, pol.ipv6, pol.hostDev)
	_ = d.installScript(ctx, script)
}

func (d *Driver) bridgeName() string {
	d.egressMu.Lock()
	defer d.egressMu.Unlock()
	if d.bridge != "" {
		return d.bridge
	}
	return sandboxBridge
}

func (d *Driver) waitAddress(ctx context.Context, id string) (ContainerState, error) {
	var last ContainerState
	var err error
	for i := 0; i < 20; i++ {
		last, err = d.client.ContainerInspect(ctx, id)
		if err == nil && last.IPv4 != "" {
			return last, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ContainerState{}, ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return ContainerState{}, err
	}
	return ContainerState{}, fmt.Errorf("sandbox has no ipv4 address")
}

func (d *Driver) renderPolicy(ctx context.Context, chain string, st ContainerState, spec driver.SandboxSpec) (string, error) {
	ip4, err := netip.ParseAddr(st.IPv4)
	if err != nil || !ip4.Is4() {
		return "", fmt.Errorf("sandbox ipv4 %q is invalid", st.IPv4)
	}
	var ip6 netip.Addr
	if st.IPv6 != "" {
		ip6, err = netip.ParseAddr(st.IPv6)
		if err != nil {
			return "", fmt.Errorf("sandbox ipv6 %q is invalid", st.IPv6)
		}
	}
	allow4, allow6, deny4, deny6, dns4, dns6, allowList, err := compileLists(ctx, spec.Policy)
	if err != nil {
		return "", err
	}
	return netpolicy.RenderSandbox(netpolicy.Sandbox{
		Chain: chain, Bridge: d.bridgeName(),
		IPv4: ip4, IPv6: ip6,
		Deny4: deny4, Deny6: deny6, Allow4: allow4, Allow6: allow6,
		DNS4: dns4, DNS6: dns6, AllowList: allowList,
		MaxConns: spec.Policy.MaxConns, EgressKbps: spec.Policy.EgressKbps, PID: st.Pid,
		Baseline: d.baseline(),
	})
}

func compileLists(ctx context.Context, policy driver.NetPolicy) (allow4, allow6, deny4, deny6, dns4, dns6 []netip.Prefix, allowList bool, err error) {
	allow, err := netpolicy.ParseList(policy.Allow)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, false, err
	}
	deny, err := netpolicy.ParseList(policy.Deny)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, false, err
	}
	if err := rejectWildcards(allow); err != nil {
		return nil, nil, nil, nil, nil, nil, false, err
	}
	if err := rejectWildcards(deny); err != nil {
		return nil, nil, nil, nil, nil, nil, false, err
	}
	allow4, allow6, err = expand(ctx, allow)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, false, err
	}
	deny4, deny6, err = expand(ctx, deny)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, false, err
	}
	for _, raw := range policy.DNS {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, nil, nil, nil, nil, nil, false, fmt.Errorf("dns resolver %q is not an IP", raw)
		}
		bits := 32
		if addr.Is6() {
			bits = 128
			dns6 = append(dns6, netip.PrefixFrom(addr, bits))
			continue
		}
		dns4 = append(dns4, netip.PrefixFrom(addr, bits))
	}
	return allow4, allow6, deny4, deny6, dns4, dns6, len(policy.Allow) > 0, nil
}

func rejectWildcards(list netpolicy.Parsed) error {
	for _, entry := range list.Entries {
		if entry.Wildcard {
			return fmt.Errorf("wildcard *.%s is not enforced by the packet filter; list each hostname", entry.Domain)
		}
	}
	return nil
}

func expand(ctx context.Context, list netpolicy.Parsed) (v4, v6 []netip.Prefix, err error) {
	v4 = append(v4, onlyFamily(list.Prefixes(), false)...)
	v6 = append(v6, onlyFamily(list.Prefixes(), true)...)
	for _, domain := range list.Domains() {
		addrs, err := lookupHost(ctx, domain)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve %s: %w", domain, err)
		}
		for _, addr := range addrs {
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			p := netip.PrefixFrom(addr, bits)
			if addr.Is6() {
				v6 = append(v6, p)
			} else {
				v4 = append(v4, p)
			}
		}
	}
	return v4, v6, nil
}

func onlyFamily(prefixes []netip.Prefix, v6 bool) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range prefixes {
		if p.Addr().Is6() == v6 {
			out = append(out, p)
		}
	}
	return out
}
