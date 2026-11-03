package core

import (
	"context"
	"errors"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

// EventNetworkUpdated is a running sandbox whose egress policy changed.
const EventNetworkUpdated = "sandbox.network_updated"

// NetworkUpdate changes the egress policy of a running or paused sandbox.
// A nil field keeps the sandbox's current value, with one exception: an
// explicit Mode of "full" without Allow inherits the allow list from the API
// key and the server again, so switching from allow to full opens egress.
type NetworkUpdate struct {
	Mode       string
	Allow      *[]string
	Deny       *[]string
	EgressKbps *int
	MaxConns   *int
}

// UpdateNetwork replaces a live sandbox's allow and deny lists and limits.
// The new policy goes through the same inheritance and API key ceiling as a
// create, using the key that owns the sandbox, so an update can never reach
// past what a fresh sandbox for that key could get. The server and key deny
// lists stay in force. Switching between none and full or allow is rejected,
// because the network interface is attached when the sandbox is created.
func (m *Manager) UpdateNetwork(ctx context.Context, actor Actor, sandboxID string, in NetworkUpdate) (store.Sandbox, error) {
	m.netMu.Lock()
	defer m.netMu.Unlock()
	sb, err := m.alive(ctx, actor, sandboxID)
	if err != nil {
		return store.Sandbox{}, err
	}
	mode := in.Mode
	if mode == "" {
		mode = sb.Network
	}
	switch mode {
	case "none", "full", "allow":
	default:
		return store.Sandbox{}, invalid("network.mode must be none, full, or allow")
	}
	if sb.Network == "none" || mode == "none" {
		if sb.Network == "none" && mode == "none" {
			return store.Sandbox{}, invalid("sandbox has no network; create it with network full or allow to set egress rules")
		}
		return store.Sandbox{}, unsupported("a running sandbox cannot switch between network none and " + nonNone(sb.Network, mode) + "; create a new sandbox")
	}
	d, err := m.backend(sb)
	if err != nil {
		return store.Sandbox{}, err
	}
	enf, ok := d.(driver.NetEnforcer)
	if !ok || !d.Capabilities().NetPolicy {
		return store.Sandbox{}, unsupported(sb.Backend + " cannot change egress rules on a running sandbox")
	}

	netIn := CreateInput{Network: mode, HasDeny: true, Deny: sb.Deny}
	switch {
	case in.Allow != nil:
		netIn.HasAllow, netIn.Allow = true, *in.Allow
	case in.Mode == "full":
		// Inherit the key or server allow list.
	default:
		netIn.HasAllow, netIn.Allow = true, sb.Allow
	}
	if in.Deny != nil {
		netIn.Deny = *in.Deny
	}
	kbps, conns := sb.EgressKbps, sb.MaxConns
	netIn.EgressKbps, netIn.MaxConns = &kbps, &conns
	if in.EgressKbps != nil {
		netIn.EgressKbps = in.EgressKbps
	}
	if in.MaxConns != nil {
		netIn.MaxConns = in.MaxConns
	}
	owner := sb.OwnerKeyID
	if owner == store.SystemOwner {
		owner = ""
	}
	newMode, _, policy, err := m.effectiveNet(ctx, Actor{KeyID: owner}, netIn)
	if errors.Is(err, store.ErrNotFound) {
		return store.Sandbox{}, conflict("the API key that owns this sandbox is gone; its network cannot change")
	}
	if err != nil {
		return store.Sandbox{}, err
	}
	if sb.ContainerID == "" {
		return store.Sandbox{}, conflict("sandbox is not running")
	}
	spec := driver.SandboxSpec{ID: sb.ID, Network: driver.NetFull, Policy: policy}
	if err := enf.ApplyNetPolicy(ctx, sb.ContainerID, spec); err != nil {
		// Put the old rules back so a half-applied script does not linger.
		m.applyStoredPolicy(ctx, sb)
		return store.Sandbox{}, m.asDriver(err)
	}
	prev := sb
	sb.Network = newMode
	sb.Allow, sb.Deny = policy.Allow, policy.Deny
	sb.EgressKbps, sb.MaxConns = policy.EgressKbps, policy.MaxConns
	if err := m.store.UpdateNetwork(ctx, sb); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// A delete stopped the sandbox meanwhile; do not leave rules behind.
			_ = enf.DropNetPolicy(ctx, sb.ContainerID)
		} else {
			m.applyStoredPolicy(ctx, prev)
		}
		return store.Sandbox{}, stale(err)
	}
	m.emit(ctx, EventNetworkUpdated, sb, map[string]any{
		"network": sb.Network, "allow": nonNilSlice(sb.Allow), "deny": nonNilSlice(sb.Deny),
		"egress_kbps": sb.EgressKbps, "max_connections": sb.MaxConns,
	})
	return sb, nil
}

func nonNone(a, b string) string {
	if a != "none" {
		return a
	}
	return b
}

func nonNilSlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
