package docker

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/netpolicy"
)

const (
	sandboxNetwork = "sbx"
	sandboxBridge  = "sbx0"
	sandboxSubnet  = "172.30.0.0/16"
	sandboxGateway = "172.30.0.1"
	helperImage    = "alpine:3.20"
)

const sandboxV6Subnet = "fd16:5b00::/64"

// egressCheckEvery is how often verifyEgress looks at the baseline rules.
var egressCheckEvery = 30 * time.Second

const sandboxV6Gateway = "fd16:5b00::1"

var bridgeNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,15}$`)

// runScriptHook applies a firewall script on the host. Tests replace it.
var runScriptHook = runHostScript

// destinationBlocked reports whether ip is metadata, link-local, or private
// under the default operator policy.
func destinationBlocked(ip netip.Addr) bool {
	return netpolicy.Blocked(ip, netpolicy.Baseline{BlockPrivate: true, BlockMetadata: true})
}

// egressScript drops blocked destinations that arrive on the sandbox bridge.
// It is applied in DOCKER-USER (forwarded traffic) and INPUT (traffic to the host).
func egressScript(bridge string) string {
	return netpolicy.RenderBaseline(bridge, netpolicy.Baseline{BlockPrivate: true, BlockMetadata: true})
}

func (d *Driver) baseline() netpolicy.Baseline {
	var allow []netip.Prefix
	for _, raw := range d.opt.PrivateAllow {
		if p, err := netip.ParsePrefix(raw); err == nil {
			allow = append(allow, p.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(raw); err == nil {
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			allow = append(allow, netip.PrefixFrom(addr, bits))
		}
	}
	return netpolicy.Baseline{
		BlockPrivate:  d.opt.BlockPrivate,
		BlockMetadata: d.opt.BlockMetadata,
		PrivateAllow:  allow,
	}
}

func nsenterCommand(script string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(script))
	return "apk add --no-cache util-linux >/dev/null && echo " + enc + " | base64 -d | nsenter -t 1 -m -n -- sh"
}

func (d *Driver) ensureEgress(ctx context.Context) error {
	d.egressMu.Lock()
	defer d.egressMu.Unlock()
	if d.egressReady {
		return nil
	}
	bridge, err := d.client.EnsureSandboxNetwork(ctx, d.wantIPv6())
	if err != nil {
		return err
	}
	d.bridge = bridge
	script := netpolicy.RenderBaseline(bridge, d.baseline())
	if err := d.installScript(ctx, script); err != nil {
		return fmt.Errorf("refusing network=full: %w", err)
	}
	d.egressReady = true
	return nil
}

// ensureFullNetwork prepares what a network=full sandbox attaches to: the
// sbx bridge, plus the egress baseline unless filtering is off.
func (d *Driver) ensureFullNetwork(ctx context.Context) error {
	if !d.opt.NoEgressFilter {
		return d.ensureEgress(ctx)
	}
	_, err := d.client.EnsureSandboxNetwork(ctx, d.wantIPv6())
	return err
}

// forgetEgress makes the next ensureEgress check the network and baseline
// again instead of trusting what it set up earlier.
func (d *Driver) forgetEgress() {
	d.egressMu.Lock()
	d.egressReady = false
	d.egressMu.Unlock()
}

// networkMissing reports whether Docker refused a container because the sbx
// network does not exist. Create answers 404 and start answers with the
// containerd error, so the message is all both have in common.
func networkMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "network "+sandboxNetwork+" not found")
}

// verifyEgress reinstalls the baseline when its rules are gone. ensureEgress
// installs them once per process, but a firewalld reload, a dockerd restart,
// or an iptables flush can remove them while sandboxes with network=full
// keep running, which would silently reopen metadata and private ranges.
// It returns whether the rules had to be reinstalled.
func (d *Driver) verifyEgress(ctx context.Context) (bool, error) {
	d.egressMu.Lock()
	defer d.egressMu.Unlock()
	if !d.egressReady {
		return false, nil
	}
	// Without host iptables each check starts a privileged helper container,
	// so the reaper's 5 second tick is too often.
	now := time.Now()
	if now.Sub(d.egressChecked) < egressCheckEvery {
		return false, nil
	}
	d.egressChecked = now
	base := d.baseline()
	if err := d.installScript(ctx, netpolicy.RenderBaselineCheck(d.bridge, base)); err == nil {
		return false, nil
	}
	if err := d.installScript(ctx, netpolicy.RenderBaseline(d.bridge, base)); err != nil {
		return true, fmt.Errorf("egress baseline is missing and could not be reinstalled: %w", err)
	}
	return true, nil
}

func (d *Driver) installScript(ctx context.Context, script string) error {
	if d.opt.ScriptRunner != nil {
		return d.opt.ScriptRunner(ctx, script)
	}
	hostErr := runScriptHook(ctx, script)
	if hostErr == nil {
		return nil
	}
	if err := d.runHelper(ctx, script); err != nil {
		return fmt.Errorf("host firewall (%v); helper: %w", hostErr, err)
	}
	return nil
}

func runHostScript(ctx context.Context, script string) error {
	if _, err := exec.LookPath("iptables"); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

func (d *Driver) runHelper(ctx context.Context, script string) error {
	if err := d.EnsureImage(ctx, helperImage); err != nil {
		return err
	}
	cmd := []string{nsenterCommand(script)}
	body := createRequest{
		Image:      helperImage,
		Entrypoint: []string{"sh", "-c"},
		Cmd:        &cmd,
		HostConfig: hostConfig{
			NetworkMode: "host",
			PidMode:     "host",
			Privileged:  true,
			CapAdd:      []string{"NET_ADMIN", "NET_RAW"},
		},
	}
	name := fmt.Sprintf("sbx-fw-%d", time.Now().UnixNano())
	id, err := d.client.ContainerCreate(ctx, name, body)
	if err != nil {
		return err
	}
	defer d.client.ContainerRemove(context.Background(), id)
	if err := d.client.ContainerStart(ctx, id); err != nil {
		return err
	}
	code, err := d.client.ContainerWait(ctx, id)
	if err != nil {
		return err
	}
	if code != 0 {
		logs, _ := d.client.ContainerLogs(ctx, id)
		logs = strings.TrimSpace(logs)
		if logs == "" {
			logs = "helper exited"
		}
		return fmt.Errorf("firewall helper exited %d: %s", code, logs)
	}
	return nil
}

// wantIPv6 reports whether a new sbx bridge should request IPv6. Docker's
// /info response has no IPv6 flag, so auto tries IPv6 and
// EnsureSandboxNetwork falls back to IPv4 when the daemon rejects it.
func (d *Driver) wantIPv6() bool {
	return d.opt.IPv6 != "off"
}

func validateNetwork(n networkInfo) (string, error) {
	for _, cfg := range n.IPAM.Config {
		sub := cfg.Subnet
		if sub == "" || strings.Contains(sub, ":") {
			continue
		}
		if sub != sandboxSubnet {
			return "", fmt.Errorf("docker network %s has subnet %s, want %s (remove the network and retry)", sandboxNetwork, sub, sandboxSubnet)
		}
	}
	bridge := n.Options["com.docker.network.bridge.name"]
	if bridge == "" && len(n.ID) >= 12 {
		bridge = "br-" + n.ID[:12]
	}
	if !bridgeNameRE.MatchString(bridge) {
		return "", fmt.Errorf("unexpected bridge name %q", bridge)
	}
	return bridge, nil
}

func sandboxNetworkSpec(ipv6 bool) map[string]any {
	v6 := "false"
	if ipv6 {
		v6 = "true"
	}
	ipam := []map[string]string{{
		"Subnet":  sandboxSubnet,
		"Gateway": sandboxGateway,
	}}
	if ipv6 {
		ipam = append(ipam, map[string]string{
			"Subnet":  sandboxV6Subnet,
			"Gateway": sandboxV6Gateway,
		})
	}
	return map[string]any{
		"Name":           sandboxNetwork,
		"Driver":         "bridge",
		"CheckDuplicate": true,
		"EnableIPv6":     ipv6,
		"Options": map[string]string{
			"com.docker.network.bridge.name":        sandboxBridge,
			"com.docker.network.bridge.enable_icc":  "false",
			"com.docker.network.bridge.enable_ipv6": v6,
		},
		"IPAM": map[string]any{
			"Driver": "default",
			"Config": ipam,
		},
	}
}
