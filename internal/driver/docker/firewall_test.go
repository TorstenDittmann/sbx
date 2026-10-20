package docker

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestWantIPv6AutoTries(t *testing.T) {
	d := &Driver{opt: Options{IPv6: "auto"}}
	if !d.wantIPv6() {
		t.Fatal("auto")
	}
	d.opt.IPv6 = "on"
	if !d.wantIPv6() {
		t.Fatal("on")
	}
	d.opt.IPv6 = "off"
	if d.wantIPv6() {
		t.Fatal("off")
	}
}

func TestDestinationBlocked(t *testing.T) {
	blocked := []string{
		"169.254.169.254", "169.254.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.255",
		"192.168.1.1", "100.100.100.200", "::1", "fe80::1", "fd00:ec2::254",
	}
	for _, raw := range blocked {
		if !destinationBlocked(netip.MustParseAddr(raw)) {
			t.Fatalf("%s should be blocked", raw)
		}
	}
	allowed := []string{"1.1.1.1", "8.8.8.8", "172.15.255.255", "172.32.0.1", "100.100.100.201", "2001:4860:4860::8888"}
	for _, raw := range allowed {
		if destinationBlocked(netip.MustParseAddr(raw)) {
			t.Fatalf("%s should be allowed", raw)
		}
	}
}

func TestEgressScriptTargetsSandboxBridge(t *testing.T) {
	script := egressScript("sbx0")
	for _, want := range []string{
		"169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.100.100.200/32",
		"fe80::/10", "fc00::/7", "DOCKER-USER", "INPUT", "-i sbx0", "REJECT", "ip6tables",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %s\n%s", want, script)
		}
	}
	if strings.Contains(egressScript("sbx0"), "-i br-") {
		t.Fatal("script should not mention another bridge")
	}
	cmd := nsenterCommand("iptables -L")
	if !strings.Contains(cmd, "nsenter -t 1 -m -n") || !strings.Contains(cmd, "base64 -d") {
		t.Fatal(cmd)
	}
}

func TestValidateNetwork(t *testing.T) {
	info := networkInfo{ID: "abcdef0123456789"}
	info.Options = map[string]string{"com.docker.network.bridge.name": "sbx0"}
	info.IPAM.Config = append(info.IPAM.Config, struct {
		Subnet  string `json:"Subnet"`
		Gateway string `json:"Gateway"`
	}{Subnet: sandboxSubnet, Gateway: sandboxGateway})
	bridge, err := validateNetwork(info)
	if err != nil || bridge != "sbx0" {
		t.Fatalf("%s %v", bridge, err)
	}
	info.Options = nil
	bridge, err = validateNetwork(info)
	if err != nil || bridge != "br-abcdef012345" {
		t.Fatalf("derived %s %v", bridge, err)
	}
	info.IPAM.Config[0].Subnet = "10.0.0.0/8"
	if _, err := validateNetwork(info); err == nil {
		t.Fatal("expected subnet mismatch")
	}
	info.IPAM.Config[0].Subnet = sandboxSubnet
	info.Options = map[string]string{"com.docker.network.bridge.name": "bad name"}
	if _, err := validateNetwork(info); err == nil {
		t.Fatal("expected bad bridge name")
	}
}

func TestEnsureEgressAndHelper(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var calls int
	d := NewWithClient(Options{
		Name: "docker", UID: 1000, GID: 1000,
		ScriptRunner: func(_ context.Context, script string) error {
			calls++
			if !strings.Contains(script, "169.254.0.0/16") || !strings.Contains(script, "-i sbx0") {
				t.Fatalf("script %s", script)
			}
			return nil
		},
	}, cli)
	if err := d.ensureEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.ensureEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	if !fd.network {
		t.Fatal("network was not created")
	}

	failing := NewWithClient(Options{
		Name: "docker", UID: 1000, GID: 1000,
		ScriptRunner: func(context.Context, string) error { return errors.New("denied") },
	}, cli)
	if err := failing.ensureEgress(ctx); err == nil || !strings.Contains(err.Error(), "refusing network=full") {
		t.Fatal(err)
	}

	prev := runScriptHook
	t.Cleanup(func() { runScriptHook = prev })
	runScriptHook = func(context.Context, string) error { return errors.New("no host iptables") }
	helper := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20}, cli)
	if err := helper.ensureEgress(ctx); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	body := fd.created
	fd.mu.Unlock()
	if !body.HostConfig.Privileged || body.HostConfig.NetworkMode != "host" || body.HostConfig.PidMode != "host" {
		t.Fatalf("helper host config %+v", body.HostConfig)
	}
	if body.Cmd == nil || len(*body.Cmd) != 1 || !strings.Contains((*body.Cmd)[0], "nsenter") {
		t.Fatalf("cmd %+v", body.Cmd)
	}

	fd.mu.Lock()
	fd.waitCode = 1
	fd.logs = "iptables: boom\n"
	fd.mu.Unlock()
	broken := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000}, cli)
	if err := broken.runHelper(ctx, "iptables -L"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatal(err)
	}
}

func TestRunHostScript(t *testing.T) {
	ctx := context.Background()
	if _, err := exec.LookPath("iptables"); err != nil {
		if err := runHostScript(ctx, "true"); err == nil {
			t.Fatal("expected a missing iptables binary to fail closed")
		}
		return
	}
	if err := runHostScript(ctx, "true"); err != nil {
		t.Fatal(err)
	}
	if err := runHostScript(ctx, "exit 9"); err == nil {
		t.Fatal("expected failure")
	}
}

func TestFilesCheckAndStream(t *testing.T) {
	_, sock := startFakeDaemon(t)
	if _, err := New(Options{Host: "tcp://127.0.0.1:1"}); err == nil {
		t.Fatal("expected unix-only host")
	}
	d, err := New(Options{Host: sock, Name: "docker", UID: 1000, GID: 1000, ReadOnlyRootfs: true, MaxOutputBytes: 1 << 20, NoEgressFilter: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if d.Name() != "docker" || !d.Capabilities().Pause {
		t.Fatal(d.Capabilities())
	}
	h, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_files000000001", Image: "alpine:3.20", CPU: 0.25, MemoryMB: 64, DiskMB: 16, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := d.ListFiles(ctx, h.ID, "/workspace")
	if err != nil || len(entries) != 3 {
		t.Fatalf("%+v %v", entries, err)
	}
	if entries[0].Type != driver.FileFile || entries[1].Type != driver.FileLink || entries[2].Type != driver.FileDir {
		t.Fatalf("%+v", entries)
	}
	body, err := d.ReadFile(ctx, h.ID, "/workspace/a.txt", 0)
	if err != nil || string(body) == "" {
		t.Fatalf("%q %v", body, err)
	}
	if err := d.WriteFile(ctx, h.ID, "/workspace/a.txt", []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(ctx, h.ID, "/"); err == nil {
		t.Fatal("expected refusal")
	}
	if err := d.RemoveFile(ctx, h.ID, "/workspace/a.txt"); err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	res, err := d.ExecStream(ctx, h.ID, driver.ExecRequest{Cmd: []string{"echo", "x"}}, func(chunk driver.ExecChunk) error {
		if chunk.Kind == driver.StreamStdout {
			streamed.Write(chunk.Data)
		}
		return nil
	})
	if err != nil || res.ExitCode != 0 || streamed.Len() == 0 {
		t.Fatalf("%q %+v %v", streamed.String(), res, err)
	}
	gv := NewWithClient(Options{Name: "gvisor", Runtime: "runsc", UID: 1000, GID: 1000}, d.client)
	if err := gv.Check(ctx); err == nil {
		t.Fatal("expected missing runsc")
	}
}

func TestVerifyEgressReinstallsMissingBaseline(t *testing.T) {
	_, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	prev := egressCheckEvery
	egressCheckEvery = 0
	t.Cleanup(func() { egressCheckEvery = prev })
	ctx := context.Background()
	intact := true
	var installs, checks int
	d := NewWithClient(Options{
		Name: "docker", UID: 1000, GID: 1000,
		ScriptRunner: func(_ context.Context, script string) error {
			if strings.Contains(script, "-C SBX-EGRESS") {
				checks++
				if !intact {
					return errors.New("rule missing")
				}
				return nil
			}
			installs++
			intact = true
			return nil
		},
	}, cli)
	if again, err := d.verifyEgress(ctx); again || err != nil || checks != 0 {
		t.Fatalf("checked before any install: %v %v %d", again, err, checks)
	}
	if err := d.ensureEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if again, err := d.verifyEgress(ctx); again || err != nil || installs != 1 {
		t.Fatalf("intact baseline: %v %v installs=%d", again, err, installs)
	}
	intact = false
	if err := d.RefreshNetPolicies(ctx); err == nil || !strings.Contains(err.Error(), "reinstalled") {
		t.Fatalf("refresh after a flush = %v", err)
	}
	if installs != 2 || !intact {
		t.Fatalf("baseline not reinstalled: installs=%d", installs)
	}
}
