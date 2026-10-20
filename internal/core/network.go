package core

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/netpolicy"
	"github.com/TorstenDittmann/sbx/internal/store"
)

func (m *Manager) effectiveNet(ctx context.Context, actor Actor, in CreateInput) (string, driver.NetMode, driver.NetPolicy, error) {
	var key store.KeyNetwork
	if actor.KeyID != "" {
		row, err := m.store.KeyByID(ctx, actor.KeyID)
		if err != nil {
			return "", "", driver.NetPolicy{}, err
		}
		key = row.Network
	}
	cfg := m.cfg.Network
	mode := cfg.DefaultMode
	if key.Mode != "" {
		mode = key.Mode
	}
	if in.Network != "" {
		mode = in.Network
	}
	if mode == "" {
		mode = "none"
	}
	// A server default looser than the key's ceiling is narrowed to it, so
	// callers that never ask for a mode keep working.
	if in.Network == "" && key.Mode == "" && key.MaxMode != "" && !ModeWithin(mode, key.MaxMode) {
		mode = key.MaxMode
	}
	switch mode {
	case "none", "full", "allow":
	default:
		return "", "", driver.NetPolicy{}, invalid("network.mode must be none, full, or allow")
	}

	allow := append([]string{}, cfg.EgressAllow...)
	if key.Allow != nil {
		allow = append([]string{}, (*key.Allow)...)
	}
	if in.HasAllow {
		allow = append([]string{}, in.Allow...)
	}
	deny := unionStrings(cfg.EgressDeny, nil)
	if key.Deny != nil {
		deny = unionStrings(deny, *key.Deny)
	}
	if in.HasDeny {
		deny = unionStrings(deny, in.Deny)
	}
	if err := rejectNetList(allow); err != nil {
		return "", "", driver.NetPolicy{}, err
	}
	if err := rejectNetList(deny); err != nil {
		return "", "", driver.NetPolicy{}, err
	}
	if mode == "allow" && len(allow) == 0 {
		return "", "", driver.NetPolicy{}, invalid("network.mode allow requires an allow list")
	}
	if err := checkKeyCeiling(key, mode, allow); err != nil {
		return "", "", driver.NetPolicy{}, err
	}
	if (in.EgressKbps != nil && *in.EgressKbps < 0) || (in.MaxConns != nil && *in.MaxConns < 0) {
		return "", "", driver.NetPolicy{}, invalid("bandwidth and connection limits must be >= 0")
	}
	kbps := tightest(cfg.DefaultEgressKbps, key.EgressKbps, intOrZero(in.EgressKbps))
	conns := tightest(cfg.DefaultMaxConns, key.MaxConns, intOrZero(in.MaxConns))
	netMode := driver.NetNone
	if mode == "none" {
		allow, deny = nil, nil
		kbps, conns = 0, 0
	} else {
		netMode = driver.NetFull
	}
	policy := driver.NetPolicy{
		Allow: allow, Deny: deny,
		BlockPrivate: cfg.BlockPrivate, BlockMetadata: cfg.BlockMetadata,
		PrivateAllow: append([]string{}, cfg.PrivateAllow...),
		DNS:          append([]string{}, cfg.DNS...),
		EgressKbps:   kbps, MaxConns: conns,
	}
	return mode, netMode, policy, nil
}

// modeRank orders network modes from most to least restrictive.
var modeRank = map[string]int{"none": 0, "allow": 1, "full": 2}

// ValidMaxMode reports whether m is an accepted key max_mode ("" is unset).
func ValidMaxMode(m string) bool {
	_, ok := modeRank[m]
	return m == "" || ok
}

// ModeWithin reports whether mode is no looser than ceiling. An empty mode
// is the server default and is checked when a sandbox is created.
func ModeWithin(mode, ceiling string) bool {
	if mode == "" {
		return true
	}
	return modeRank[mode] <= modeRank[ceiling]
}

// checkKeyCeiling enforces a key's max_mode. Without one, the key network is
// only a default the sandbox may override. With max_mode "allow", every
// sandbox allow entry must fall inside the key's allow list.
func checkKeyCeiling(key store.KeyNetwork, mode string, allow []string) error {
	if key.MaxMode == "" {
		return nil
	}
	if modeRank[mode] > modeRank[key.MaxMode] {
		return forbidden(fmt.Sprintf("network mode %s exceeds this API key's max_mode %s", mode, key.MaxMode))
	}
	if mode != "allow" || key.MaxMode != "allow" {
		return nil
	}
	var limit []string
	if key.Allow != nil {
		limit = *key.Allow
	}
	for _, item := range allow {
		if !allowCovered(item, limit) {
			return forbidden(fmt.Sprintf("allow entry %s is outside this API key's allow list", item))
		}
	}
	return nil
}

// allowCovered reports whether item is in limit: the same hostname, or an
// address or CIDR inside one of the limit's CIDRs.
func allowCovered(item string, limit []string) bool {
	norm := func(v string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".") }
	want := norm(item)
	inner, innerErr := parsePrefix(want)
	for _, l := range limit {
		have := norm(l)
		if have == want {
			return true
		}
		if innerErr != nil {
			continue
		}
		outer, err := parsePrefix(have)
		if err == nil && outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr()) {
			return true
		}
	}
	return false
}

func parsePrefix(v string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(v); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// sameNet reports whether two effective network settings are equal. The warm
// pool uses it so a claim never hands out a looser policy than the
// claimant's key would get on a fresh create.
func sameNet(modeA string, a driver.NetPolicy, modeB string, b driver.NetPolicy) bool {
	return modeA == modeB && sameStrings(a.Allow, b.Allow) && sameStrings(a.Deny, b.Deny) &&
		a.EgressKbps == b.EgressKbps && a.MaxConns == b.MaxConns
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		if seen[v] == 0 {
			return false
		}
		seen[v]--
	}
	return true
}

func unionStrings(lists ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, list := range lists {
		for _, item := range list {
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func tightest(vals ...int) int {
	out := 0
	for _, v := range vals {
		if v <= 0 {
			continue
		}
		if out == 0 || v < out {
			out = v
		}
	}
	return out
}

func intOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func rejectNetList(items []string) error {
	parsed, err := netpolicy.ParseList(items)
	if err != nil {
		return invalid(err.Error())
	}
	for _, entry := range parsed.Entries {
		if entry.Wildcard {
			return invalid("wildcard domains are not enforced by the packet filter; list each hostname")
		}
	}
	return nil
}
