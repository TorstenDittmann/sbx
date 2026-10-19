package docker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/id"
	"github.com/TorstenDittmann/sbx/internal/netpolicy"
)

func TestIntegrationInternet(t *testing.T) {
	skipIntegration(t)
	runWorkload(t, "", true)
}

func TestIntegrationInternetGVisor(t *testing.T) {
	skipIntegration(t)
	if !runtimeRegistered(t, "runsc") {
		t.Skip("runsc is not registered with Docker")
	}
	runWorkload(t, "runsc", false)
}

func TestIntegrationAllowList(t *testing.T) {
	skipIntegration(t)
	runAllowList(t, "")
}

func TestIntegrationAllowListGVisor(t *testing.T) {
	skipIntegration(t)
	if !runtimeRegistered(t, "runsc") {
		t.Skip("runsc is not registered with Docker")
	}
	runAllowList(t, "runsc")
}

func TestIntegrationBandwidthRule(t *testing.T) {
	skipIntegration(t)
	if _, err := exec.LookPath("iptables"); err != nil {
		t.Skip("iptables is not installed")
	}
	if _, err := exec.LookPath("tc"); err != nil {
		t.Skip("tc is not installed")
	}
	d := newNetDriver(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sbID := id.Sandbox()
	h, err := d.Create(ctx, driver.SandboxSpec{
		ID: sbID, Image: "alpine:3.20", CPU: 0.5, MemoryMB: 64, DiskMB: 32, PIDs: 64,
		Network: driver.NetFull,
		Policy: driver.NetPolicy{
			DNS: []string{"1.1.1.1", "8.8.8.8"}, BlockPrivate: true, BlockMetadata: true,
			EgressKbps: 20000, MaxConns: 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	chain, err := netpolicy.ChainName(sbID)
	if err != nil {
		t.Fatal(err)
	}
	iptables := "iptables"
	args := []string{"-S", chain}
	if os.Geteuid() != 0 {
		iptables = "sudo"
		args = append([]string{"-n", "iptables"}, args...)
	}
	out, err := exec.Command(iptables, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("iptables %v %s", err, out)
	}
	if !strings.Contains(string(out), "connlimit-above 20") {
		t.Fatalf("chain rules:\n%s", out)
	}
}

func runWorkload(t *testing.T, runtime string, full bool) {
	t.Helper()
	d := newNetDriver(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	spec := netSpec("alpine:3.20", 2048, 2048)
	h, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	assertResolvers(t, d, ctx, h.ID)
	assertIPv6WhenEnabled(t, h.ID)
	script := shortWorkload
	if full {
		script = fullWorkload
	}
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", script}, TimeoutSeconds: 600})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("workload exit %d err %v\nstdout:\n%s\nstderr:\n%s", res.ExitCode, err, res.Stdout, res.Stderr)
	}
	if full {
		runApt(t, d)
	}
}

func runAllowList(t *testing.T, runtime string) {
	t.Helper()
	d := newNetDriver(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	spec := netSpec("alpine:3.20", 128, 64)
	spec.Policy.Allow = []string{"1.1.1.1/32"}
	spec.Policy.DNS = []string{"1.1.1.1"}
	h, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	ok, err := d.Exec(ctx, h.ID, driver.ExecRequest{
		Cmd: []string{"sh", "-c", httpProbe("1.1.1.1")}, TimeoutSeconds: 20,
	})
	if err != nil || ok.ExitCode != 0 || !strings.Contains(string(ok.Stdout), "HTTP/") {
		t.Fatalf("allowed %+v %v", ok, err)
	}
	blocked, err := d.Exec(ctx, h.ID, driver.ExecRequest{
		Cmd: []string{"sh", "-c", httpProbe("1.0.0.1")}, TimeoutSeconds: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.ExitCode == 0 && strings.Contains(string(blocked.Stdout), "HTTP/") {
		t.Fatal("1.0.0.1 was reachable through an allow list of 1.1.1.1/32")
	}
}

func httpProbe(host string) string {
	return "printf 'GET / HTTP/1.0\\r\\nHost: " + host + "\\r\\n\\r\\n' | nc -w 8 " + host + " 80"
}

func runApt(t *testing.T, d *Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	spec := netSpec("debian:bookworm-slim", 1024, 1024)
	h, err := d.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Delete(context.Background(), h.ID) })
	script := `set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl python3-pip
curl -fsSL --max-time 30 -o /workspace/example.html https://example.com/
test -s /workspace/example.html
pip3 install --target /workspace/py six==1.16.0
PYTHONPATH=/workspace/py python3 -c 'import six'
`
	res, err := d.Exec(ctx, h.ID, driver.ExecRequest{Cmd: []string{"sh", "-c", script}, TimeoutSeconds: 420})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("apt exit %d err %v\nstdout:\n%s\nstderr:\n%s", res.ExitCode, err, res.Stdout, res.Stderr)
	}
}

func newNetDriver(t *testing.T, runtime string) *Driver {
	t.Helper()
	opt := Options{Name: "docker", ReadOnlyRootfs: false, Root: true, MaxOutputBytes: 4 << 20}
	if runtime != "" {
		opt.Name = "gvisor"
		opt.Runtime = runtime
	}
	d, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func netSpec(image string, memory, disk int) driver.SandboxSpec {
	return driver.SandboxSpec{
		ID: id.Sandbox(), Image: image, CPU: 1, MemoryMB: memory, DiskMB: disk, PIDs: 1024,
		Network: driver.NetFull,
		Policy: driver.NetPolicy{
			DNS: []string{"1.1.1.1", "8.8.8.8"}, BlockPrivate: true, BlockMetadata: true,
		},
	}
}

func assertResolvers(t *testing.T, d *Driver, ctx context.Context, id string) {
	t.Helper()
	res, err := d.Exec(ctx, id, driver.ExecRequest{Cmd: []string{"cat", "/etc/resolv.conf"}, TimeoutSeconds: 20})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("resolv %+v %v", res, err)
	}
	text := string(res.Stdout)
	if !strings.Contains(text, "1.1.1.1") || !strings.Contains(text, "8.8.8.8") {
		t.Fatalf("resolv.conf:\n%s", text)
	}
}

func assertIPv6WhenEnabled(t *testing.T, id string) {
	t.Helper()
	netRaw, err := exec.Command("docker", "network", "inspect", sandboxNetwork).Output()
	if err != nil {
		t.Fatal(err)
	}
	var nets []struct {
		EnableIPv6 bool `json:"EnableIPv6"`
		IPAM       struct {
			Config []struct {
				Subnet string `json:"Subnet"`
			} `json:"Config"`
		} `json:"IPAM"`
	}
	if err := json.Unmarshal(netRaw, &nets); err != nil {
		t.Fatal(err)
	}
	hasV6 := false
	for _, n := range nets {
		if n.EnableIPv6 {
			hasV6 = true
		}
		for _, cfg := range n.IPAM.Config {
			if strings.Contains(cfg.Subnet, ":") {
				hasV6 = true
			}
		}
	}
	if !hasV6 {
		return
	}
	raw, err := exec.Command("docker", "inspect", id).Output()
	if err != nil {
		t.Fatal(err)
	}
	var inspected []struct {
		NetworkSettings struct {
			Networks map[string]struct {
				GlobalIPv6Address string
			}
		}
	}
	if err := json.Unmarshal(raw, &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected) == 0 {
		t.Fatal("empty inspect")
	}
	addr := ""
	for _, n := range inspected[0].NetworkSettings.Networks {
		if n.GlobalIPv6Address != "" {
			addr = n.GlobalIPv6Address
		}
	}
	if addr == "" {
		t.Fatal("docker reports ipv6 but the sandbox has no address")
	}
}

func runtimeRegistered(t *testing.T, name string) bool {
	t.Helper()
	d, err := New(Options{Name: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := d.client.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, ok := info.Runtimes[name]
	return ok
}

func skipIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("SBX_INTEGRATION") != "1" {
		t.Skip("set SBX_INTEGRATION=1 to run against a Docker daemon")
	}
}

const shortWorkload = `set -eu
apk add --no-cache curl git ca-certificates
curl -fsSL --max-time 30 -o /workspace/example.html https://example.com/
test -s /workspace/example.html
git ls-remote https://github.com/octocat/Hello-World.git HEAD
curl -fsSL --max-time 60 -o /workspace/down.bin -L https://github.com/cli/cli/releases/download/v2.58.0/gh_2.58.0_checksums.txt
test -s /workspace/down.bin
`

const fullWorkload = `set -eu
apk add --no-cache curl git openssh-client python3 py3-pip nodejs npm go ca-certificates
curl -fsSL --max-time 30 -o /workspace/example.html https://example.com/
test -s /workspace/example.html
curl -fsSL --max-time 90 -L -o /workspace/down.bin https://github.com/cli/cli/releases/download/v2.58.0/gh_2.58.0_linux_amd64.tar.gz
test "$(wc -c < /workspace/down.bin)" -ge 1000000
python3 - <<'PY'
import socket, time
s = socket.create_connection(("1.1.1.1", 443), 20)
time.sleep(2)
s.sendall(b"GET / HTTP/1.0\r\nHost: one.one.one.one\r\n\r\n")
data = s.recv(32)
if not data:
    raise SystemExit("empty long connection")
print("long-ok")
PY
git clone --depth 1 https://github.com/octocat/Hello-World.git /workspace/hello
test -f /workspace/hello/README
python3 - <<'PY'
import socket
s = socket.create_connection(("github.com", 22), 20)
data = s.recv(64)
if not data.startswith(b"SSH-"):
    raise SystemExit(data)
print(data.decode("ascii", "replace"))
PY
npm view is-odd version --fetch-timeout=60000
pip3 install --target /workspace/py six==1.16.0
PYTHONPATH=/workspace/py python3 -c 'import six'
mkdir -p /workspace/gomod
cd /workspace/gomod
go mod init example.com/sbxnet
GOPROXY=https://proxy.golang.org GOSUMDB=off GOCACHE=/workspace/gocache GOPATH=/workspace/gopath go get github.com/google/uuid@v1.6.0
curl -fsSL --max-time 90 -L -r 0-1048575 -o /workspace/bun.part https://github.com/oven-sh/bun/releases/download/bun-v1.1.34/bun-linux-x64.zip || curl -fsSL --max-time 90 -L -o /workspace/bun.part https://bun.sh/install
test -s /workspace/bun.part
`
