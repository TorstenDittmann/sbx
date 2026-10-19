package core

import (
	"context"

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
