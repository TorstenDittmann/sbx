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
)

const (
	sandboxNetwork = "sbx"
	sandboxBridge  = "sbx0"
	sandboxSubnet  = "172.30.0.0/16"
	sandboxGateway = "172.30.0.1"
	helperImage    = "alpine:3.20"
)

// blockedIPv4 are destinations a sandbox must not reach even on network=full.
// Link-local covers the metadata address used by AWS, GCP, Azure, and others.
// 100.100.100.200 is the Alibaba Cloud metadata service.
var blockedIPv4 = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"100.100.100.200/32",
}

var blockedIPv6 = []string{
	"fe80::/10",
	"fc00::/7",
	"::1/128",
}

var bridgeNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,15}$`)

// runScriptHook applies a firewall script on the host. Tests replace it.
var runScriptHook = runHostScript

func blockedPrefixes() []netip.Prefix {
	all := append(append([]string{}, blockedIPv4...), blockedIPv6...)
	out := make([]netip.Prefix, 0, len(all))
	for _, cidr := range all {
		out = append(out, netip.MustParsePrefix(cidr))
	}
	return out
}

// destinationBlocked reports whether ip is metadata, link-local, or private.
func destinationBlocked(ip netip.Addr) bool {
	for _, prefix := range blockedPrefixes() {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// egressScript drops blocked destinations that arrive on the sandbox bridge.
// It is applied in DOCKER-USER (forwarded traffic) and INPUT (traffic to the host).
func egressScript(bridge string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	writeFamily(&b, "iptables", bridge, "icmp-host-prohibited", blockedIPv4)
	b.WriteString("if command -v ip6tables >/dev/null 2>&1 && ip6tables -w 5 -L DOCKER-USER -n >/dev/null 2>&1; then\n")
	writeFamily(&b, "ip6tables", bridge, "icmp6-adm-prohibited", blockedIPv6)
	b.WriteString("fi\n")
	return b.String()
}

func writeFamily(b *strings.Builder, bin, bridge, reject string, cidrs []string) {
	fmt.Fprintf(b, "%s -w 5 -N SBX-EGRESS 2>/dev/null || true\n", bin)
	fmt.Fprintf(b, "%s -w 5 -F SBX-EGRESS\n", bin)
	for _, cidr := range cidrs {
		fmt.Fprintf(b, "%s -w 5 -A SBX-EGRESS -d %s -j REJECT --reject-with %s\n", bin, cidr, reject)
	}
	fmt.Fprintf(b, "%s -w 5 -A SBX-EGRESS -j RETURN\n", bin)
	fmt.Fprintf(b, "%s -w 5 -C DOCKER-USER -i %s -j SBX-EGRESS 2>/dev/null || %s -w 5 -I DOCKER-USER 1 -i %s -j SBX-EGRESS\n", bin, bridge, bin, bridge)
	fmt.Fprintf(b, "%s -w 5 -C INPUT -i %s -j SBX-EGRESS 2>/dev/null || %s -w 5 -I INPUT 1 -i %s -j SBX-EGRESS\n", bin, bridge, bin, bridge)
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
	bridge, err := d.client.EnsureSandboxNetwork(ctx)
	if err != nil {
		return err
	}
	script := egressScript(bridge)
	if err := d.installScript(ctx, script); err != nil {
		return fmt.Errorf("refusing network=full: %w", err)
	}
	d.egressReady = true
	return nil
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
	body := createRequest{
		Image:      helperImage,
		Entrypoint: []string{"sh", "-c"},
		Cmd:        []string{nsenterCommand(script)},
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

func validateNetwork(n networkInfo) (string, error) {
	if len(n.IPAM.Config) > 0 {
		sub := n.IPAM.Config[0].Subnet
		if sub != "" && sub != sandboxSubnet {
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

func sandboxNetworkSpec() map[string]any {
	return map[string]any{
		"Name":           sandboxNetwork,
		"Driver":         "bridge",
		"CheckDuplicate": true,
		"Options": map[string]string{
			"com.docker.network.bridge.name":        sandboxBridge,
			"com.docker.network.bridge.enable_icc":  "false",
			"com.docker.network.bridge.enable_ipv6": "false",
		},
		"IPAM": map[string]any{
			"Driver": "default",
			"Config": []map[string]string{{
				"Subnet":  sandboxSubnet,
				"Gateway": sandboxGateway,
			}},
		},
	}
}
